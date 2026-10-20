// Package localproxy is the free, no-VPS, no-admin mode: an HTTP + SOCKS5
// proxy on loopback. Every NEW outgoing TCP connection is dialed out of one
// network adapter (pinned with bindif), chosen by weighted least-load, so
// multi-connection traffic uses all links at once. A single TCP connection
// always stays on one adapter.
package localproxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"mergeinternet/internal/bindif"
	"mergeinternet/internal/config"
)

const (
	dialTimeout    = 10 * time.Second
	healthTimeout  = 5 * time.Second
	healthEvery    = 10 * time.Second
	stickyTTL      = 10 * time.Minute
	stickyMax      = 4096
	stopWait       = 5 * time.Second
	maxPaths       = 256
	copyChunk      = 32 * 1024
	handshakeLimit = 30 * time.Second
)

var errNoPath = errors.New("no enabled path")

var healthTargets = []string{"1.1.1.1:443", "8.8.8.8:443"}

// Options configures Start.
type Options struct {
	HTTPAddr  string        // e.g. "127.0.0.1:7879": HTTP proxy (CONNECT + absolute-URI requests)
	SOCKSAddr string        // e.g. "127.0.0.1:7880": SOCKS5 (CONNECT only, no auth); "" = off
	Paths     []config.Path // initial paths; path ID = index
	Sticky    bool          // same destination host -> same path for 10 min
	Logf      func(format string, args ...any)

	// test hooks
	dial        func(ctx context.Context, pathID uint8, network, addr string) (net.Conn, error)
	noHealth    bool
	healthEvery time.Duration
}

// PathStats is a snapshot of one path.
type PathStats struct {
	ID          uint8
	Alive       bool          // last health check succeeded
	RTT         time.Duration // TCP connect time of the last health check
	RxBytes     uint64        // bytes received from the internet on this path (cumulative)
	TxBytes     uint64        // bytes sent to the internet on this path
	ActiveConns int
	Enabled     bool
	LimitMbps   float64
}

type pathState struct {
	id      uint8
	cfg     config.Path // guarded by Proxy.mu
	enabled atomic.Bool
	alive   atomic.Bool
	rtt     atomic.Int64
	rx, tx  atomic.Uint64
	active  atomic.Int64
	lim     limiter
}

type stickyEntry struct {
	id      uint8
	expires time.Time
}

// Proxy is a running local proxy.
type Proxy struct {
	opts Options
	logf func(string, ...any)
	dial func(ctx context.Context, pathID uint8, network, addr string) (net.Conn, error)

	mu      sync.Mutex // guards paths, sticky, conns, stopped, cfg fields
	paths   []*pathState
	sticky  map[string]stickyEntry
	conns   map[io.Closer]struct{}
	stopped bool

	httpLn, socksLn net.Listener
	srv             *http.Server
	tr              *http.Transport

	baseCtx  context.Context
	cancel   context.CancelFunc
	stopCh   chan struct{}
	done     chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup
	errV     atomic.Value // error
}

func checkLoopback(addr string) (string, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", fmt.Errorf("listen address %q: %w", addr, err)
	}
	if host == "localhost" {
		host = "127.0.0.1"
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || !ip.IsLoopback() {
		return "", fmt.Errorf("listen address %q is not a loopback address; the local proxy only listens on 127.0.0.1 / ::1", addr)
	}
	return net.JoinHostPort(host, port), nil
}

// Start listens and starts health checks.
func Start(opts Options) (*Proxy, error) {
	if opts.HTTPAddr == "" {
		return nil, errors.New("localproxy: HTTPAddr is required")
	}
	httpAddr, err := checkLoopback(opts.HTTPAddr)
	if err != nil {
		return nil, err
	}
	socksAddr := ""
	if opts.SOCKSAddr != "" {
		if socksAddr, err = checkLoopback(opts.SOCKSAddr); err != nil {
			return nil, err
		}
	}
	if len(opts.Paths) > maxPaths {
		return nil, fmt.Errorf("localproxy: at most %d paths", maxPaths)
	}
	p := &Proxy{
		opts:   opts,
		logf:   opts.Logf,
		sticky: map[string]stickyEntry{},
		conns:  map[io.Closer]struct{}{},
		stopCh: make(chan struct{}),
		done:   make(chan struct{}),
	}
	p.baseCtx, p.cancel = context.WithCancel(context.Background())
	if p.logf == nil {
		p.logf = func(string, ...any) {}
	}
	p.dial = opts.dial
	if p.dial == nil {
		p.dial = p.defaultDial
	}
	for _, c := range opts.Paths {
		p.addPathLocked(c)
	}
	p.httpLn, err = net.Listen("tcp", httpAddr)
	if err != nil {
		return nil, fmt.Errorf("listen %s: %w", httpAddr, err)
	}
	if socksAddr != "" {
		if p.socksLn, err = net.Listen("tcp", socksAddr); err != nil {
			p.httpLn.Close()
			return nil, fmt.Errorf("listen %s: %w", socksAddr, err)
		}
	}
	p.tr = &http.Transport{
		DialContext:         p.dialBalanced,
		DisableCompression:  true,
		MaxIdleConns:        64,
		MaxIdleConnsPerHost: 2,
		IdleConnTimeout:     30 * time.Second,
		Proxy:               nil,
	}
	p.srv = &http.Server{Handler: http.HandlerFunc(p.serveHTTP), ReadHeaderTimeout: 30 * time.Second}

	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		if err := p.srv.Serve(p.httpLn); err != nil && !errors.Is(err, http.ErrServerClosed) {
			select {
			case <-p.stopCh:
			default:
				p.fail(err)
			}
		}
	}()
	if p.socksLn != nil {
		p.wg.Add(1)
		go p.socksLoop()
	}
	if !opts.noHealth {
		every := opts.healthEvery
		if every <= 0 {
			every = healthEvery
		}
		for _, ps := range p.paths {
			p.startHealth(ps, every)
		}
	}
	return p, nil
}

func (p *Proxy) fail(err error) {
	p.errV.CompareAndSwap(nil, err)
	p.logf("localproxy: fatal: %v", err)
	go p.Stop()
}

// Err returns the error that stopped the proxy, if any.
func (p *Proxy) Err() error {
	if e, _ := p.errV.Load().(error); e != nil {
		return e
	}
	return nil
}

// Done is closed once the proxy has stopped.
func (p *Proxy) Done() <-chan struct{} { return p.done }

// HTTPAddr is the actual bound HTTP proxy address.
func (p *Proxy) HTTPAddr() string { return p.httpLn.Addr().String() }

// SOCKSAddr is the actual bound SOCKS5 address ("" when off).
func (p *Proxy) SOCKSAddr() string {
	if p.socksLn == nil {
		return ""
	}
	return p.socksLn.Addr().String()
}

// Stop closes listeners and all proxied connections. Idempotent.
func (p *Proxy) Stop() {
	p.stopOnce.Do(func() {
		p.mu.Lock()
		p.stopped = true
		close(p.stopCh)
		p.mu.Unlock()
		p.cancel()

		p.httpLn.Close()
		if p.socksLn != nil {
			p.socksLn.Close()
		}
		p.srv.Close()
		p.tr.CloseIdleConnections()
		p.closeAll()

		fin := make(chan struct{})
		go func() { p.wg.Wait(); close(fin) }()
		select {
		case <-fin:
		case <-time.After(stopWait):
			p.logf("localproxy: stop: goroutines still running after %s", stopWait)
		}
		// connections registered while shutting down
		p.closeAll()
		close(p.done)
	})
	<-p.done
}

func (p *Proxy) closeAll() {
	p.mu.Lock()
	cs := make([]io.Closer, 0, len(p.conns))
	for c := range p.conns {
		cs = append(cs, c)
	}
	p.mu.Unlock()
	for _, c := range cs {
		c.Close()
	}
}

// begin registers a goroutine; false when stopping.
func (p *Proxy) begin() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stopped {
		return false
	}
	p.wg.Add(1)
	return true
}

// track registers a closer to be closed on Stop. ok=false means already stopped (c is closed).
func (p *Proxy) track(c io.Closer) (untrack func(), ok bool) {
	p.mu.Lock()
	if p.stopped {
		p.mu.Unlock()
		c.Close()
		return func() {}, false
	}
	p.conns[c] = struct{}{}
	p.mu.Unlock()
	return func() {
		p.mu.Lock()
		delete(p.conns, c)
		p.mu.Unlock()
	}, true
}

// ---- paths ----

func (p *Proxy) addPathLocked(c config.Path) *pathState {
	ps := &pathState{id: uint8(len(p.paths)), cfg: c}
	ps.enabled.Store(true)
	ps.alive.Store(true) // optimistic until the first health check says otherwise
	ps.lim.setMbps(c.LimitMbps)
	p.paths = append(p.paths, ps)
	return ps
}

// AddPath hot-adds a path and returns its ID.
func (p *Proxy) AddPath(cfg config.Path) (uint8, error) {
	p.mu.Lock()
	if p.stopped {
		p.mu.Unlock()
		return 0, errors.New("localproxy: stopped")
	}
	if len(p.paths) >= maxPaths {
		p.mu.Unlock()
		return 0, fmt.Errorf("localproxy: at most %d paths", maxPaths)
	}
	ps := p.addPathLocked(cfg)
	p.mu.Unlock()
	if !p.opts.noHealth {
		every := p.opts.healthEvery
		if every <= 0 {
			every = healthEvery
		}
		p.startHealth(ps, every)
	}
	return ps.id, nil
}

func (p *Proxy) get(id uint8) (*pathState, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if int(id) >= len(p.paths) {
		return nil, fmt.Errorf("localproxy: unknown path %d", id)
	}
	return p.paths[id], nil
}

// SetEnabled: disabled paths get no new connections; existing ones finish.
func (p *Proxy) SetEnabled(id uint8, on bool) error {
	ps, err := p.get(id)
	if err != nil {
		return err
	}
	ps.enabled.Store(on)
	return nil
}

// SetLimit caps the path's throughput in each direction combined (token bucket); 0 = unlimited.
func (p *Proxy) SetLimit(id uint8, mbps float64) error {
	ps, err := p.get(id)
	if err != nil {
		return err
	}
	if mbps < 0 {
		mbps = 0
	}
	p.mu.Lock()
	ps.cfg.LimitMbps = mbps
	p.mu.Unlock()
	ps.lim.setMbps(mbps)
	return nil
}

// SetWeight sets the balancing weight (the path's download Mbps).
func (p *Proxy) SetWeight(id uint8, downMbps float64) error {
	ps, err := p.get(id)
	if err != nil {
		return err
	}
	p.mu.Lock()
	ps.cfg.DownMbps = downMbps
	p.mu.Unlock()
	return nil
}

// Stats returns a snapshot sorted by ID.
func (p *Proxy) Stats() []PathStats {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]PathStats, len(p.paths))
	for i, ps := range p.paths {
		out[i] = PathStats{
			ID:          ps.id,
			Alive:       ps.alive.Load(),
			RTT:         time.Duration(ps.rtt.Load()),
			RxBytes:     ps.rx.Load(),
			TxBytes:     ps.tx.Load(),
			ActiveConns: int(ps.active.Load()),
			Enabled:     ps.enabled.Load(),
			LimitMbps:   ps.cfg.LimitMbps,
		}
	}
	return out
}

// ---- health ----

func (p *Proxy) startHealth(ps *pathState, every time.Duration) {
	if !p.begin() {
		return
	}
	go func() {
		defer p.wg.Done()
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			p.checkPath(ps)
			select {
			case <-p.stopCh:
				return
			case <-t.C:
			}
		}
	}()
}

func (p *Proxy) checkPath(ps *pathState) {
	for _, addr := range healthTargets {
		ctx, cancel := context.WithTimeout(p.baseCtx, healthTimeout)
		start := time.Now()
		c, err := p.dial(ctx, ps.id, "tcp", addr)
		rtt := time.Since(start)
		cancel()
		if err == nil {
			c.Close()
			ps.rtt.Store(int64(rtt))
			ps.alive.Store(true)
			return
		}
	}
	if ps.alive.Swap(false) {
		p.logf("path %d: health check failed, marked down", ps.id)
	}
}

// ---- balancing ----

func weightOf(c config.Path) float64 {
	w := c.DownMbps
	if c.LimitMbps > 0 && (w <= 0 || c.LimitMbps < w) {
		w = c.LimitMbps
	}
	if w <= 0 {
		w = 1
	}
	return w
}

// choose picks the best path and reserves a connection slot on it.
func (p *Proxy) choose(host string, tried map[uint8]bool) *pathState {
	p.mu.Lock()
	defer p.mu.Unlock()
	var enabled, alive []*pathState
	for _, ps := range p.paths {
		if !ps.enabled.Load() || tried[ps.id] {
			continue
		}
		enabled = append(enabled, ps)
		if ps.alive.Load() {
			alive = append(alive, ps)
		}
	}
	cands := alive
	if len(cands) == 0 {
		cands = enabled
	}
	if len(cands) == 0 {
		return nil
	}
	var pick *pathState
	if p.opts.Sticky && host != "" {
		if e, ok := p.sticky[host]; ok {
			if time.Now().Before(e.expires) {
				for _, ps := range cands {
					if ps.id == e.id {
						pick = ps
					}
				}
			} else {
				delete(p.sticky, host)
			}
		}
	}
	if pick == nil {
		best := 0.0
		for _, ps := range cands {
			score := float64(ps.active.Load()+1) / weightOf(ps.cfg)
			if pick == nil || score < best {
				pick, best = ps, score
			}
		}
	}
	pick.active.Add(1)
	if p.opts.Sticky && host != "" {
		p.stickyLocked(host, pick.id)
	}
	return pick
}

// stickyLocked records host -> id for stickyTTL (p.mu held).
func (p *Proxy) stickyLocked(host string, id uint8) {
	now := time.Now()
	if e, ok := p.sticky[host]; ok && now.Before(e.expires) && e.id == id {
		return
	}
	if len(p.sticky) >= stickyMax {
		for k, e := range p.sticky {
			if !now.Before(e.expires) {
				delete(p.sticky, k)
			}
		}
		for k := range p.sticky {
			if len(p.sticky) < stickyMax {
				break
			}
			delete(p.sticky, k)
		}
	}
	p.sticky[host] = stickyEntry{id: id, expires: now.Add(stickyTTL)}
}

func (p *Proxy) defaultDial(ctx context.Context, id uint8, network, addr string) (net.Conn, error) {
	ps, err := p.get(id)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	iface := ps.cfg.Interface
	p.mu.Unlock()
	d, _, err := bindif.DialerFor(iface)
	if err != nil {
		return nil, err
	}
	d.Timeout = dialTimeout
	return d.DialContext(ctx, "tcp4", addr)
}

// dialBalanced dials addr on the best path, retrying once on the next best.
func (p *Proxy) dialBalanced(ctx context.Context, network, addr string) (net.Conn, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	host = strings.ToLower(host)
	tried := map[uint8]bool{}
	lastErr := errNoPath
	for attempt := 0; attempt < 2; attempt++ {
		ps := p.choose(host, tried)
		if ps == nil {
			break
		}
		tried[ps.id] = true
		dctx, cancel := context.WithTimeout(ctx, dialTimeout)
		c, err := p.dial(dctx, ps.id, "tcp", addr)
		cancel()
		if err != nil {
			ps.active.Add(-1)
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			lastErr = fmt.Errorf("path %d: %w", ps.id, err)
			if ps.alive.Swap(false) {
				p.logf("path %d: dial %s failed (%v), marked down", ps.id, addr, err)
			}
			continue
		}
		pc := &pconn{Conn: c, ps: ps, p: p}
		var ok bool
		pc.untrack, ok = p.track(pc)
		if !ok {
			return nil, errors.New("localproxy: stopped")
		}
		return pc, nil
	}
	return nil, lastErr
}

// pconn counts bytes and applies the path rate limit; Close releases the slot.
type pconn struct {
	net.Conn
	ps      *pathState
	p       *Proxy
	once    sync.Once
	untrack func()
}

func (c *pconn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	if n > 0 {
		c.ps.rx.Add(uint64(n))
		c.ps.lim.wait(n, c.p.stopCh)
	}
	return n, err
}

func (c *pconn) Write(b []byte) (int, error) {
	total := 0
	for len(b) > 0 {
		chunk := b
		if len(chunk) > copyChunk {
			chunk = chunk[:copyChunk]
		}
		c.ps.lim.wait(len(chunk), c.p.stopCh)
		n, err := c.Conn.Write(chunk)
		c.ps.tx.Add(uint64(n))
		total += n
		if err != nil {
			return total, err
		}
		b = b[n:]
	}
	return total, nil
}

func (c *pconn) CloseWrite() error {
	if cw, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return cw.CloseWrite()
	}
	return nil
}

func (c *pconn) Close() error {
	err := c.Conn.Close()
	c.once.Do(func() {
		c.ps.active.Add(-1)
		if c.untrack != nil {
			c.untrack()
		}
	})
	return err
}

// tunnel pipes client <-> up until both directions finish.
func tunnel(client, up net.Conn, pre []byte) {
	if len(pre) > 0 {
		if _, err := up.Write(pre); err != nil {
			client.Close()
			up.Close()
			return
		}
	}
	var wg sync.WaitGroup
	wg.Add(2)
	cp := func(dst, src net.Conn) {
		defer wg.Done()
		if _, err := io.Copy(dst, src); err != nil {
			client.Close()
			up.Close()
			return
		}
		if cw, ok := dst.(interface{ CloseWrite() error }); ok {
			cw.CloseWrite()
		}
	}
	go cp(up, client)
	go cp(client, up)
	wg.Wait()
	client.Close()
	up.Close()
}

// refuseDest reports destinations the proxy must never connect to (itself / loopback).
func refuseDest(hostport string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = strings.ToLower(strings.Trim(host, "[]"))
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		ip = ip.Unmap()
		return ip.IsLoopback() || ip.IsUnspecified()
	}
	return false
}
