package client

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"mergeinternet/internal/config"
	"mergeinternet/internal/localproxy"
	"mergeinternet/internal/sysproxy"
)

// Addresses of the local proxy in no-VPS mode (loopback only).
const (
	LocalHTTPAddr  = "127.0.0.1:7879"
	LocalSOCKSAddr = "127.0.0.1:7880"
)

// Local is the no-VPS mode: a proxy on 127.0.0.1 that is made the Windows
// system proxy and sends every new connection out of one of the paths.
// It needs no server and no administrator rights.
type Local struct {
	cfg       *config.Client
	px        *localproxy.Proxy
	lg        *ledger
	sys       *sysproxy.Manager
	cancel    context.CancelFunc
	startedAt time.Time

	wg       sync.WaitGroup
	done     chan struct{}
	stopOnce sync.Once
	errMu    sync.Mutex
	err      error
}

var _ Runner = (*Local)(nil)

type localBackend struct{ px *localproxy.Proxy }

func (b localBackend) enable(id uint8, on bool)    { _ = b.px.SetEnabled(id, on) }
func (b localBackend) limit(id uint8, kbps uint32) { _ = b.px.SetLimit(id, float64(kbps)/1000) }
func (b localBackend) totals() map[uint8]uint64 {
	stats := b.px.Stats()
	out := make(map[uint8]uint64, len(stats))
	for _, s := range stats {
		out[s.ID] = s.RxBytes + s.TxBytes
	}
	return out
}

// StartLocal starts the proxy and, if sys is non-nil, makes it the system proxy.
func StartLocal(cfg *config.Client, usage *Usage, sys *sysproxy.Manager) (*Local, error) {
	px, err := localproxy.Start(localproxy.Options{
		HTTPAddr:  LocalHTTPAddr,
		SOCKSAddr: LocalSOCKSAddr,
		Paths:     cfg.Paths,
		Sticky:    cfg.Sticky,
		Logf:      log.Printf,
	})
	if err != nil {
		return nil, fmt.Errorf("không mở được proxy cục bộ: %w", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	l := &Local{cfg: cfg, px: px, lg: newLedger(usage, localBackend{px}), sys: sys, cancel: cancel,
		startedAt: time.Now(), done: make(chan struct{})}
	l.lg.mu.Lock()
	for _, p := range cfg.Paths {
		l.lg.addLocked(p)
	}
	l.lg.mu.Unlock()
	if sys != nil {
		if err := sys.Install(px.HTTPAddr()); err != nil {
			cancel()
			px.Stop()
			return nil, err
		}
	}
	l.goRun(func() { l.lg.run(ctx) })
	l.goRun(func() {
		select {
		case <-ctx.Done():
		case <-px.Done():
			l.fail(fmt.Errorf("proxy cục bộ dừng: %v", px.Err()))
		}
	})
	log.Printf("local mode up: %d paths, proxy %s (SOCKS5 %s)", len(cfg.Paths), px.HTTPAddr(), LocalSOCKSAddr)
	return l, nil
}

func (l *Local) Mode() string { return config.ConnLocal }

func (l *Local) AddPath(p config.Path) (uint8, error) {
	l.lg.mu.Lock()
	defer l.lg.mu.Unlock()
	if l.lg.hasInterfaceLocked(p.Interface) {
		return 0, fmt.Errorf("card %q đã được dùng", p.Interface)
	}
	id, err := l.px.AddPath(p)
	if err != nil {
		return 0, err
	}
	if r := l.lg.addLocked(p); r.id != id {
		return 0, errors.New("lệch số thứ tự đường mạng giữa proxy và sổ sách")
	}
	log.Printf("path %s: added while running (%s)", p.Name, p.Interface)
	return id, nil
}

func (l *Local) SetEnabled(id uint8, on bool) error { return l.lg.SetEnabled(id, on) }
func (l *Local) SetLimits(id uint8, limitMbps, quotaGB float64) error {
	return l.lg.SetLimits(id, limitMbps, quotaGB)
}
func (l *Local) ResetUsage(id uint8) error { return l.lg.ResetUsage(id) }

func (l *Local) SetWeights(id uint8, downMbps, upMbps float64) error {
	l.lg.mu.Lock()
	defer l.lg.mu.Unlock()
	if _, err := l.lg.setWeightsLocked(id, downMbps, upMbps); err != nil {
		return err
	}
	return l.px.SetWeight(id, downMbps)
}

func (l *Local) Measure(ctx context.Context, ids []uint8, dur time.Duration, progress func(MeasureResult)) []MeasureResult {
	l.lg.mu.Lock()
	paths := l.lg.configsLocked(ids)
	l.lg.mu.Unlock()
	out := MeasureLocal(ctx, paths, dur, progress)
	for i := range out {
		out[i].Index = int(ids[i])
	}
	return out
}

func (l *Local) Paths() []PathInfo {
	stats := l.px.Stats()
	l.lg.mu.Lock()
	out := l.lg.snapshotLocked()
	l.lg.mu.Unlock()
	for i := range out {
		for _, s := range stats {
			if s.ID == out[i].ID {
				out[i].Alive, out[i].RTT, out[i].RxBytes, out[i].TxBytes = s.Alive, s.RTT, s.RxBytes, s.TxBytes
			}
		}
	}
	return out
}

func (l *Local) StartedAt() time.Time { return l.startedAt }

func (l *Local) goRun(f func()) {
	l.wg.Add(1)
	go func() { defer l.wg.Done(); f() }()
}

func (l *Local) fail(err error) {
	l.errMu.Lock()
	if l.err == nil {
		l.err = err
	}
	l.errMu.Unlock()
	go l.Stop()
}

// Stop restores the system proxy first (so the user is never left pointing at
// a dead proxy), then closes the proxy. Safe to call more than once.
func (l *Local) Stop() {
	l.stopOnce.Do(func() {
		if l.sys != nil {
			if err := l.sys.Restore(); err != nil {
				log.Printf("restore system proxy: %v", err)
			}
		}
		l.cancel()
		l.px.Stop()
		waited := make(chan struct{})
		go func() { l.wg.Wait(); close(waited) }()
		select {
		case <-waited:
		case <-time.After(5 * time.Second):
			log.Printf("local mode stop: workers did not exit within 5s")
		}
		log.Printf("local mode down, system proxy restored")
		close(l.done)
	})
}

func (l *Local) Done() <-chan struct{} { return l.done }

func (l *Local) Err() error {
	l.errMu.Lock()
	defer l.errMu.Unlock()
	return l.err
}

// MeasureLocal measures paths against a public speed-test service (no VPS
// needed). Paths are measured concurrently.
func MeasureLocal(ctx context.Context, paths []config.Path, dur time.Duration, progress func(MeasureResult)) []MeasureResult {
	out := make([]MeasureResult, len(paths))
	var wg sync.WaitGroup
	var mu sync.Mutex
	for i, p := range paths {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := MeasureResult{Index: i, Interface: p.Interface}
			r.DownMbps, r.UpMbps, r.Err = localproxy.MeasurePath(ctx, p.Interface, dur)
			mu.Lock()
			out[i] = r
			if progress != nil {
				progress(r)
			}
			mu.Unlock()
		}()
	}
	wg.Wait()
	return out
}
