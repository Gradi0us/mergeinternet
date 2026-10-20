// Package engine is the bonding core shared by client and server: it seals
// packets, spreads them across links, and turns what arrives back into an
// ordered packet stream.
package engine

import (
	"context"
	"errors"
	"net/netip"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"mergeinternet/internal/proto"
	"mergeinternet/internal/reorder"
	"mergeinternet/internal/sched"
	"mergeinternet/internal/window"
)

type Role int

const (
	Client Role = iota
	Server
)

const (
	probeInterval = 500 * time.Millisecond
	healthTick    = 250 * time.Millisecond
	flushTick     = 5 * time.Millisecond
	// A link with nothing received for this long is taken out of the schedule.
	DeadAfter = 2500 * time.Millisecond
)

type Config struct {
	Role         Role
	Key          []byte
	Mode         sched.Mode
	ReorderDelay time.Duration
	// Deliver writes a received IP packet to the local TUN device. Calls are serialized.
	Deliver func(pkt []byte) error
	Logf    func(format string, args ...any)
}

// Link is one physical path (e.g. fibre, Android tether, iPhone tether).
type Link struct {
	ID   uint8
	Name string

	send     func([]byte) error
	sendKbps atomic.Uint32 // weight for packets this side sends on the link
	peerKbps atomic.Uint32 // client only: what the server should use when sending down this link
	// Speed cap in kbit/s, 0 = none. Client: the configured cap (upload here,
	// advertised for download). Server: the cap the client last advertised.
	limitKbps atomic.Uint32

	remote   atomic.Pointer[netip.AddrPort] // server only: learned client address for this link
	lastRecv atomic.Int64
	rtt      atomic.Int64
	txBytes  atomic.Uint64
	rxBytes  atomic.Uint64
	txErrs   atomic.Uint64
	disabled atomic.Bool // client only: user switched the link off at runtime
}

type LinkStats struct {
	ID       uint8
	Name     string
	Alive    bool
	Enabled  bool
	RTT      time.Duration
	TxBytes  uint64
	RxBytes  uint64
	TxErrors uint64
	Remote   string
	// LimitKbps is the link's speed cap (0 = unlimited). On the server it is
	// the value last advertised by the client.
	LimitKbps uint32
}

type Engine struct {
	cfg     Config
	cipher  *proto.Cipher
	ourDir  proto.Dir
	peerDir proto.Dir

	sessionMu sync.Mutex
	session   atomic.Uint64
	counter   atomic.Uint64
	seq       atomic.Uint64
	replay    window.Window
	reorder   *reorder.Buffer
	sched     *sched.Scheduler

	mu    sync.RWMutex
	links map[uint8]*Link

	running atomic.Bool // Run is active; lets AddLink probe a hot-added link at once

	serverSend func(b []byte, to netip.AddrPort) error
	boot       uint64        // server: start time, sent in probe acks
	peerBoot   atomic.Uint64 // client: last boot ID seen from the server

	rxBad    atomic.Uint64
	rxReplay atomic.Uint64

	runCtx atomic.Pointer[context.Context] // Run's ctx; speed floods stop with it
	speed  speedState
	// speedCapMbps overrides the default speed-test rate cap (0 = default); tests only.
	speedCapMbps uint16
}

func newEngine(cfg Config) (*Engine, error) {
	if cfg.Deliver == nil {
		return nil, errors.New("engine: Deliver is required")
	}
	if cfg.Logf == nil {
		cfg.Logf = func(string, ...any) {}
	}
	c, err := proto.NewCipher(cfg.Key)
	if err != nil {
		return nil, err
	}
	e := &Engine{cfg: cfg, cipher: c, sched: sched.New(cfg.Mode), links: map[uint8]*Link{}}
	e.reorder = reorder.New(cfg.ReorderDelay, 0, func(pkt []byte) {
		if err := cfg.Deliver(pkt); err != nil {
			cfg.Logf("deliver: %v", err)
		}
	})
	return e, nil
}

// NewClient creates the client side. Its session ID is the start time in
// nanoseconds, so a restarted client always outranks (and replaces) its
// previous session on the server, and nonces are never reused across restarts.
func NewClient(cfg Config) (*Engine, error) {
	cfg.Role = Client
	e, err := newEngine(cfg)
	if err != nil {
		return nil, err
	}
	e.ourDir, e.peerDir = proto.ClientToServer, proto.ServerToClient
	e.session.Store(uint64(time.Now().UnixNano()))
	return e, nil
}

// NewServer creates the server side; send writes a datagram to a client address.
func NewServer(cfg Config, send func(b []byte, to netip.AddrPort) error) (*Engine, error) {
	cfg.Role = Server
	e, err := newEngine(cfg)
	if err != nil {
		return nil, err
	}
	e.ourDir, e.peerDir = proto.ServerToClient, proto.ClientToServer
	e.serverSend = send
	// The server keeps a client's session across its own restarts, so its
	// counter must never restart: start it at the boot time in nanoseconds
	// (faster than any packet rate) so nonces stay unique.
	e.boot = uint64(time.Now().UnixNano())
	e.counter.Store(e.boot)
	return e, nil
}

// rotateSession starts a fresh client session (used after the server restarted,
// so both sides restart sequence numbering together).
func (e *Engine) rotateSession() {
	e.sessionMu.Lock()
	defer e.sessionMu.Unlock()
	s := uint64(time.Now().UnixNano())
	if s <= e.session.Load() {
		s = e.session.Load() + 1
	}
	e.replay.Reset()
	e.reorder.Reset()
	e.seq.Store(0)
	e.counter.Store(0)
	e.session.Store(s)
	e.cfg.Logf("server restarted; new session %016x", s)
}

// AddLink registers a client link. upKbps weights client→server traffic on it,
// downKbps is advertised to the server to weight server→client traffic.
func (e *Engine) AddLink(id uint8, name string, upKbps, downKbps uint32, send func([]byte) error) *Link {
	l := &Link{ID: id, Name: name, send: send}
	l.peerKbps.Store(downKbps)
	l.sendKbps.Store(upKbps)
	e.mu.Lock()
	if old := e.links[id]; old != nil {
		l.limitKbps.Store(old.limitKbps.Load()) // the scheduler keeps the cap, so must the link
	}
	e.links[id] = l
	e.mu.Unlock()
	e.sched.Set(id, float64(upKbps), false)
	// Safe while Run is active: the link table is lock-protected, the scheduler
	// is internally locked and peerKbps is written before the link is published.
	// The link joins the schedule on its first probe ack; probe right away when
	// running instead of waiting up to probeInterval for the next tick.
	if e.running.Load() && e.cfg.Role == Client && e.session.Load() != 0 {
		e.sealAndSend(l, proto.TypeProbe, proto.PutProbe(time.Now().UnixNano(), l.peerKbps.Load(), l.limitKbps.Load()))
	}
	return l
}

// SetLinkLimit caps a client link to kbps kbit/s (0 = unlimited) in both
// directions: it limits upload through the local scheduler and is advertised
// in every probe so the server caps download on the same link. Returns false
// if the link does not exist.
func (e *Engine) SetLinkLimit(id uint8, kbps uint32) bool {
	l := e.link(id)
	if l == nil {
		return false
	}
	l.limitKbps.Store(kbps)
	e.sched.SetLimit(id, kbps)
	return true
}

// Dropped counts packets discarded because every live link was at its speed cap.
func (e *Engine) Dropped() uint64 { return e.sched.Dropped() }

// SetLinkEnabled switches a client link on or off at runtime. A disabled link
// sends nothing (no data, no probes), so the server stops using it for
// downloads once it times out (DeadAfter); packets still arriving on it are accepted.
func (e *Engine) SetLinkEnabled(id uint8, enabled bool) bool {
	l := e.link(id)
	if l == nil {
		return false
	}
	l.disabled.Store(!enabled)
	e.sched.Set(id, float64(l.sendKbps.Load()), enabled && l.Alive(time.Now()))
	return true
}

func (e *Engine) link(id uint8) *Link {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.links[id]
}

// SendPacket tunnels one IP packet read from the local TUN device.
func (e *Engine) SendPacket(pkt []byte) {
	if e.session.Load() == 0 {
		return // server before any client has connected
	}
	var ids [8]uint8
	// The cap is on what goes over the wire, so charge the tunnel overhead too.
	picked := e.sched.Pick(len(pkt)+proto.DataOverhead, ids[:0])
	if len(picked) == 0 {
		// Policed: drop WITHOUT consuming a sequence number, otherwise the
		// receiver would stall every later packet for ReorderDelay on the gap.
		return
	}
	payload := proto.PutData(e.seq.Add(1), pkt)
	for _, id := range picked {
		if l := e.link(id); l != nil {
			e.sealAndSend(l, proto.TypeData, payload)
		}
	}
}

func (e *Engine) sealAndSend(l *Link, t proto.Type, payload []byte) {
	h := proto.Header{Type: t, PathID: l.ID, Dir: e.ourDir, Session: e.session.Load(), Counter: e.counter.Add(1)}
	b := e.cipher.Seal(make([]byte, 0, proto.Overhead+len(payload)), h, payload)
	if err := l.send(b); err != nil {
		l.txErrs.Add(1)
		return
	}
	l.txBytes.Add(uint64(len(b)))
}

// Inbound handles one datagram received from the network. raw is not retained.
func (e *Engine) Inbound(raw []byte, from netip.AddrPort) {
	h, pt, err := e.cipher.Open(nil, raw)
	if err != nil || h.Dir != e.peerDir {
		e.rxBad.Add(1)
		return
	}
	if !e.acceptSession(h.Session) {
		e.rxBad.Add(1)
		return
	}
	if !e.replay.Check(h.Counter) {
		e.rxReplay.Add(1) // replayed, duplicated, or far older than the window
		return
	}
	l := e.inboundLink(h.PathID, from)
	if l == nil {
		e.rxBad.Add(1)
		return
	}
	now := time.Now()
	wasAlive := l.Alive(now)
	l.lastRecv.Store(now.UnixNano())
	l.rxBytes.Add(uint64(len(raw)))
	if !wasAlive && !l.disabled.Load() {
		// Use a new or recovered link right away instead of on the next health tick.
		e.sched.Set(l.ID, float64(l.sendKbps.Load()), true)
	}

	switch h.Type {
	case proto.TypeData:
		seq, pkt, err := proto.ParseData(pt)
		if err == nil {
			e.reorder.Push(seq, pkt, now)
		}
	case proto.TypeProbe:
		if e.cfg.Role != Server {
			return
		}
		if _, kbps, limit, err := proto.ParseProbe(pt); err == nil {
			if l.sendKbps.Swap(kbps) != kbps {
				e.sched.Set(l.ID, float64(kbps), true)
			}
			if l.limitKbps.Swap(limit) != limit {
				e.sched.SetLimit(l.ID, limit)
			}
			e.sealAndSend(l, proto.TypeProbeAck, proto.PutProbeAck(pt, e.boot))
		}
	case proto.TypeSpeedReq, proto.TypeSpeedFill, proto.TypeSpeedRes:
		// Never delivered to the TUN and never enters the reorder buffer.
		e.handleSpeed(l, h.Type, pt, len(raw), now)
	case proto.TypeProbeAck:
		if e.cfg.Role != Client {
			return
		}
		sent, boot, err := proto.ParseProbeAck(pt)
		if err != nil {
			return
		}
		l.rtt.Store(now.UnixNano() - sent)
		if prev := e.peerBoot.Swap(boot); prev != 0 && prev != boot {
			e.rotateSession()
		}
	}
}

func (e *Engine) acceptSession(s uint64) bool {
	if e.cfg.Role == Client {
		return s == e.session.Load()
	}
	cur := e.session.Load()
	if s == cur {
		return true
	}
	if s < cur {
		return false // an older client instance (or a replay of one)
	}
	e.sessionMu.Lock()
	defer e.sessionMu.Unlock()
	if s > e.session.Load() {
		// The counter is left running (see NewServer); only the data sequence restarts.
		e.replay.Reset()
		e.reorder.Reset()
		e.seq.Store(0)
		e.session.Store(s)
		e.cfg.Logf("new client session %016x", s)
	}
	return s == e.session.Load()
}

func (e *Engine) inboundLink(id uint8, from netip.AddrPort) *Link {
	if e.cfg.Role == Client {
		return e.link(id)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	l := e.links[id]
	if l == nil {
		l = &Link{ID: id, Name: "path-" + strconv.Itoa(int(id))}
		l.send = func(b []byte) error {
			addr := l.remote.Load()
			if addr == nil {
				return errors.New("no address yet")
			}
			return e.serverSend(b, *addr)
		}
		e.links[id] = l
		e.cfg.Logf("link %d first seen from %s", id, from)
	}
	if cur := l.remote.Load(); cur == nil || *cur != from {
		if cur != nil {
			e.cfg.Logf("link %d moved %s -> %s", id, *cur, from)
		}
		l.remote.Store(&from)
	}
	return l
}

// Run drives timers (probes, link health, reorder flush) until ctx is done.
func (e *Engine) Run(ctx context.Context) {
	flush := time.NewTicker(flushTick)
	health := time.NewTicker(healthTick)
	probe := time.NewTicker(probeInterval)
	defer flush.Stop()
	defer health.Stop()
	defer probe.Stop()
	e.running.Store(true)
	defer e.running.Store(false)
	e.runCtx.Store(&ctx)
	if e.cfg.Role == Client {
		e.sendProbes()
	}
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-flush.C:
			e.reorder.Flush(now)
		case now := <-health.C:
			e.updateHealth(now)
		case <-probe.C:
			if e.cfg.Role == Client {
				e.sendProbes()
			}
		}
	}
}

func (e *Engine) sendProbes() {
	now := time.Now().UnixNano()
	for _, l := range e.snapshotLinks() {
		if !l.disabled.Load() {
			e.sealAndSend(l, proto.TypeProbe, proto.PutProbe(now, l.peerKbps.Load(), l.limitKbps.Load()))
		}
	}
}

func (e *Engine) updateHealth(now time.Time) {
	for _, l := range e.snapshotLinks() {
		e.sched.Set(l.ID, float64(l.sendKbps.Load()), l.Alive(now) && !l.disabled.Load())
	}
}

// SetLinkWeights updates a client link's weights at runtime: upKbps feeds the
// local scheduler (client→server), downKbps is advertised in the next probe so
// the server weights server→client traffic. Returns false for an unknown link
// or on the server.
func (e *Engine) SetLinkWeights(id uint8, upKbps, downKbps uint32) bool {
	l := e.link(id)
	if l == nil || e.cfg.Role != Client {
		return false
	}
	l.peerKbps.Store(downKbps)
	l.sendKbps.Store(upKbps)
	e.sched.Set(id, float64(upKbps), l.Alive(time.Now()) && !l.disabled.Load())
	return true
}

// Enabled reports whether the user has the link switched on.
func (l *Link) Enabled() bool { return !l.disabled.Load() }

// Alive reports whether anything authenticated arrived on the link recently.
func (l *Link) Alive(now time.Time) bool {
	last := l.lastRecv.Load()
	return last != 0 && now.Sub(time.Unix(0, last)) < DeadAfter
}

func (e *Engine) snapshotLinks() []*Link {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make([]*Link, 0, len(e.links))
	for _, l := range e.links {
		out = append(out, l)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (e *Engine) Stats() ([]LinkStats, reorder.Stats, uint64) {
	now := time.Now()
	var out []LinkStats
	for _, l := range e.snapshotLinks() {
		s := LinkStats{
			ID: l.ID, Name: l.Name, Alive: l.Alive(now), Enabled: !l.disabled.Load(), RTT: time.Duration(l.rtt.Load()),
			TxBytes: l.txBytes.Load(), RxBytes: l.rxBytes.Load(), TxErrors: l.txErrs.Load(), LimitKbps: l.limitKbps.Load(),
		}
		if r := l.remote.Load(); r != nil {
			s.Remote = r.String()
		}
		out = append(out, s)
	}
	return out, e.reorder.Stats(), e.rxBad.Load()
}
