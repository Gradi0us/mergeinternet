// Package client runs a connection on the user's machine, in one of two modes:
//
//   - Tunnel (VPS mode): TUN device, one pinned UDP socket per path, routes;
//     every packet is bonded through mi-server.
//   - Local (no-VPS mode): a local proxy that sends each new connection out of
//     one of the paths.
//
// Both share per-path speed caps, data quotas and on/off switches (ledger).
package client

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"mergeinternet/internal/bindif"
	"mergeinternet/internal/config"
	"mergeinternet/internal/engine"
	"mergeinternet/internal/tunif"
)

const (
	usageTick  = time.Second
	usageFlush = 15 * time.Second
)

// Tunnel is the VPS mode.
type Tunnel struct {
	cfg       *config.Client
	dev       *tunif.Device
	eng       *engine.Engine
	lg        *ledger
	ctx       context.Context
	cancel    context.CancelFunc
	cleanup   func()
	startedAt time.Time

	wg       sync.WaitGroup
	done     chan struct{}
	stopOnce sync.Once
	errMu    sync.Mutex
	err      error
}

var _ Runner = (*Tunnel)(nil)

type tunnelBackend struct{ eng *engine.Engine }

func (b tunnelBackend) enable(id uint8, on bool)    { b.eng.SetLinkEnabled(id, on) }
func (b tunnelBackend) limit(id uint8, kbps uint32) { b.eng.SetLinkLimit(id, kbps) }
func (b tunnelBackend) totals() map[uint8]uint64 {
	links, _, _ := b.eng.Stats()
	out := make(map[uint8]uint64, len(links))
	for _, l := range links {
		out[l.ID] = l.RxBytes + l.TxBytes
	}
	return out
}

// Start brings the VPS tunnel up. On success all IPv4 traffic of the machine
// goes through it. usage may be nil (no quota accounting across restarts).
func Start(cfg *config.Client, usage *Usage) (*Tunnel, error) {
	if !cfg.HasServer() {
		return nil, errors.New("chưa có máy chủ VPS trong cấu hình")
	}
	dev, err := tunif.Open(cfg.Tun.Name, cfg.Tun.MTU)
	if err != nil {
		return nil, err
	}
	eng, err := engine.NewClient(engine.Config{
		Key:          cfg.KeyBytes,
		Mode:         cfg.SchedMode,
		ReorderDelay: cfg.Reorder,
		Deliver:      dev.Write,
		Logf:         log.Printf,
	})
	if err != nil {
		dev.Close()
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	t := &Tunnel{cfg: cfg, dev: dev, eng: eng, lg: newLedger(usage, tunnelBackend{eng}), ctx: ctx, cancel: cancel,
		done: make(chan struct{}), startedAt: time.Now()}

	t.lg.mu.Lock()
	for _, p := range cfg.Paths {
		t.addPathLocked(p)
	}
	t.lg.mu.Unlock()

	cleanup, err := tunif.ConfigureClient(dev.Name(), cfg.TunPrefix, cfg.TunGateway, cfg.Tun.MTU, cfg.DNSAddrs)
	if err != nil {
		cancel()
		dev.Close()
		t.wg.Wait()
		return nil, fmt.Errorf("configure %s: %w", dev.Name(), err)
	}
	t.cleanup = cleanup

	log.Printf("tunnel up: %d paths -> %s, mode=%s, reorder=%s, tun=%s %s",
		len(cfg.Paths), cfg.ServerAddr, cfg.SchedMode, cfg.Reorder, dev.Name(), cfg.TunPrefix)

	t.goRun(func() { eng.Run(ctx) })
	t.goRun(func() { t.lg.run(ctx) })
	go func() {
		for {
			pkt, err := dev.Read()
			if err != nil {
				if ctx.Err() == nil {
					t.fail(fmt.Errorf("tun read: %w", err))
				}
				return
			}
			if len(pkt) > 0 && pkt[0]>>4 == 4 { // IPv4 only
				eng.SendPacket(pkt)
			}
		}
	}()
	return t, nil
}

// addPathLocked registers a path with the engine and the ledger and starts its socket.
func (t *Tunnel) addPathLocked(p config.Path) {
	id := uint8(len(t.lg.rows))
	pc := &pathConn{path: p, server: t.cfg.ServerAddr, eng: t.eng}
	pc.link = t.eng.AddLink(id, p.Name, kbps(p.UpMbps), kbps(p.DownMbps), pc.send)
	t.lg.addLocked(p)
	ctx := t.ctx
	t.goRun(func() { pc.supervise(ctx) })
}

func (t *Tunnel) Mode() string { return config.ConnVPS }

// AddPath hot-adds a link (e.g. a phone plugged in after start) and returns its ID.
func (t *Tunnel) AddPath(p config.Path) (uint8, error) {
	t.lg.mu.Lock()
	defer t.lg.mu.Unlock()
	if len(t.lg.rows) >= 255 {
		return 0, errors.New("too many paths")
	}
	if t.lg.hasInterfaceLocked(p.Interface) {
		return 0, fmt.Errorf("card %q đã được dùng", p.Interface)
	}
	t.addPathLocked(p)
	log.Printf("path %s: added while running (%s)", p.Name, p.Interface)
	return uint8(len(t.lg.rows) - 1), nil
}

func (t *Tunnel) SetEnabled(id uint8, on bool) error { return t.lg.SetEnabled(id, on) }
func (t *Tunnel) SetLimits(id uint8, limitMbps, quotaGB float64) error {
	return t.lg.SetLimits(id, limitMbps, quotaGB)
}
func (t *Tunnel) ResetUsage(id uint8) error { return t.lg.ResetUsage(id) }

// SetWeights applies measured capacities to a running path.
func (t *Tunnel) SetWeights(id uint8, downMbps, upMbps float64) error {
	t.lg.mu.Lock()
	defer t.lg.mu.Unlock()
	if _, err := t.lg.setWeightsLocked(id, downMbps, upMbps); err != nil {
		return err
	}
	t.eng.SetLinkWeights(id, kbps(upMbps), kbps(downMbps))
	return nil
}

// Measure tests links of the running tunnel (traffic keeps flowing; the test
// briefly competes with it).
func (t *Tunnel) Measure(ctx context.Context, ids []uint8, dur time.Duration, progress func(MeasureResult)) []MeasureResult {
	t.lg.mu.Lock()
	paths := t.lg.configsLocked(ids)
	t.lg.mu.Unlock()
	return measureLinks(ctx, t.eng, ids, paths, dur, progress)
}

// Paths returns every path with its user state and live link stats.
func (t *Tunnel) Paths() []PathInfo {
	links, _, _ := t.eng.Stats()
	t.lg.mu.Lock()
	out := t.lg.snapshotLocked()
	t.lg.mu.Unlock()
	for i := range out {
		for _, l := range links {
			if l.ID == out[i].ID {
				out[i].Alive, out[i].RTT, out[i].RxBytes, out[i].TxBytes = l.Alive, l.RTT, l.RxBytes, l.TxBytes
			}
		}
	}
	return out
}

func (t *Tunnel) goRun(f func()) {
	t.wg.Add(1)
	go func() { defer t.wg.Done(); f() }()
}

func (t *Tunnel) fail(err error) {
	t.errMu.Lock()
	if t.err == nil {
		t.err = err
	}
	t.errMu.Unlock()
	go t.Stop()
}

// Stop removes routes and closes the TUN device. Safe to call more than once.
func (t *Tunnel) Stop() {
	t.stopOnce.Do(func() {
		t.cancel()
		if t.cleanup != nil {
			t.cleanup()
		}
		t.dev.Close()
		waited := make(chan struct{})
		go func() { t.wg.Wait(); close(waited) }()
		select {
		case <-waited:
		case <-time.After(5 * time.Second):
			log.Printf("tunnel stop: workers did not exit within 5s")
		}
		log.Printf("tunnel down")
		close(t.done)
	})
}

// Done is closed once the tunnel has fully stopped (by Stop or by a fatal error).
func (t *Tunnel) Done() <-chan struct{} { return t.done }

func (t *Tunnel) Err() error {
	t.errMu.Lock()
	defer t.errMu.Unlock()
	return t.err
}

func (t *Tunnel) Engine() *engine.Engine { return t.eng }
func (t *Tunnel) Config() *config.Client { return t.cfg }
func (t *Tunnel) StartedAt() time.Time   { return t.startedAt }
func (t *Tunnel) TunName() string        { return t.dev.Name() }

func kbps(mbps float64) uint32 { return uint32(mbps * 1000) }

// pathConn keeps one UDP socket pinned to one interface, re-creating it when
// the interface disappears or changes address (e.g. phone re-plugged).
type pathConn struct {
	path   config.Path
	server netip.AddrPort
	eng    *engine.Engine
	link   *engine.Link
	conn   atomic.Pointer[net.UDPConn]
}

var errNotConnected = errors.New("path socket not open")

func (pc *pathConn) send(b []byte) error {
	c := pc.conn.Load()
	if c == nil {
		return errNotConnected
	}
	_, err := c.WriteToUDPAddrPort(b, pc.server)
	return err
}

const rebindAfter = 10 * time.Second

func (pc *pathConn) supervise(ctx context.Context) {
	for ctx.Err() == nil {
		conn, local, err := bindif.ListenUDP(ctx, pc.path.Interface)
		if err != nil {
			log.Printf("path %s: %v (retrying)", pc.path.Name, err)
			if !sleep(ctx, 3*time.Second) {
				return
			}
			continue
		}
		log.Printf("path %s: bound to %q (%s)", pc.path.Name, pc.path.Interface, local)
		engine.SetSocketBuffers(conn)
		pc.conn.Store(conn)
		readDone := make(chan struct{})
		go pc.readLoop(conn, readDone)
		pc.watch(ctx, readDone)
		pc.conn.CompareAndSwap(conn, nil)
		conn.Close()
		<-readDone
	}
}

// watch returns when ctx ends, the socket fails, or the link has been dead
// long enough that its interface probably changed.
func (pc *pathConn) watch(ctx context.Context, readDone <-chan struct{}) {
	bound := time.Now()
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-readDone:
			log.Printf("path %s: socket closed, re-binding", pc.path.Name)
			return
		case now := <-t.C:
			if pc.link.Enabled() && !pc.link.Alive(now) && now.Sub(bound) > rebindAfter {
				log.Printf("path %s: no reply for %s, re-binding", pc.path.Name, rebindAfter)
				return
			}
		}
	}
}

func (pc *pathConn) readLoop(conn *net.UDPConn, done chan<- struct{}) {
	defer close(done)
	buf := make([]byte, 65535)
	for {
		n, from, err := conn.ReadFromUDPAddrPort(buf)
		if err != nil {
			return
		}
		if from.Addr().Unmap() != pc.server.Addr() || from.Port() != pc.server.Port() {
			continue
		}
		pc.eng.Inbound(buf[:n], from)
	}
}

func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
