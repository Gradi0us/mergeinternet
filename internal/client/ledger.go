package client

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"

	"mergeinternet/internal/config"
)

var (
	ErrNoPath       = errors.New("không có đường mạng này")
	ErrQuotaReached = errors.New("đường này đã dùng hết dung lượng cho phép; tăng hạn mức hoặc đặt lại số liệu trước")
)

// PathInfo is a snapshot of one path for the UI, in either connection mode.
type PathInfo struct {
	ID         uint8
	Path       config.Path
	Enabled    bool
	Capped     bool
	Alive      bool
	RTT        time.Duration
	RxBytes    uint64 // this session
	TxBytes    uint64
	UsageBytes uint64 // persisted, counts toward the quota
	UsageSince time.Time
}

// Runner is a running connection: the VPS tunnel or the local proxy.
type Runner interface {
	Mode() string // config.ConnVPS or config.ConnLocal
	Paths() []PathInfo
	AddPath(p config.Path) (uint8, error)
	SetEnabled(id uint8, on bool) error
	SetLimits(id uint8, limitMbps, quotaGB float64) error
	SetWeights(id uint8, downMbps, upMbps float64) error
	ResetUsage(id uint8) error
	Measure(ctx context.Context, ids []uint8, dur time.Duration, progress func(MeasureResult)) []MeasureResult
	StartedAt() time.Time
	Stop()
	Done() <-chan struct{}
	Err() error
}

// backend is what a ledger needs from the transport underneath.
type backend interface {
	enable(id uint8, on bool)
	limit(id uint8, kbps uint32)
	// totals returns cumulative rx+tx bytes per path ID.
	totals() map[uint8]uint64
}

// ledger keeps per-path user state shared by both modes: on/off, speed cap,
// data usage and quota. Quota is enforced by switching the path off.
type ledger struct {
	mu    sync.Mutex
	usage *Usage
	be    backend
	rows  []*pathRow // index == path ID
}

type pathRow struct {
	id      uint8
	cfg     config.Path
	counted uint64 // backend bytes already added to usage
	userOff bool   // switched off by the user
	capped  bool   // switched off because the quota was reached
}

func newLedger(usage *Usage, be backend) *ledger {
	if usage == nil {
		usage = LoadUsage("")
	}
	return &ledger{usage: usage, be: be}
}

// addLocked registers the next path ID; the caller must have created it in the backend.
func (lg *ledger) addLocked(p config.Path) *pathRow {
	r := &pathRow{id: uint8(len(lg.rows)), cfg: p}
	lg.rows = append(lg.rows, r)
	if l := p.LimitKbps(); l > 0 {
		lg.be.limit(r.id, l)
	}
	used, _ := lg.usage.Get(p.Interface)
	if q := p.QuotaBytes(); q > 0 && used >= q {
		r.capped = true
		lg.be.enable(r.id, false)
		log.Printf("path %s: data quota already used up (%d MB of %d MB), kept off", p.Name, used/1e6, q/1e6)
	}
	return r
}

func (lg *ledger) rowLocked(id uint8) (*pathRow, error) {
	if int(id) >= len(lg.rows) {
		return nil, ErrNoPath
	}
	return lg.rows[id], nil
}

func (lg *ledger) hasInterfaceLocked(iface string) bool {
	for _, r := range lg.rows {
		if r.cfg.Interface == iface {
			return true
		}
	}
	return false
}

func (lg *ledger) SetEnabled(id uint8, on bool) error {
	lg.mu.Lock()
	defer lg.mu.Unlock()
	r, err := lg.rowLocked(id)
	if err != nil {
		return err
	}
	if on && r.capped {
		return ErrQuotaReached
	}
	r.userOff = !on
	lg.be.enable(id, on)
	return nil
}

func (lg *ledger) SetLimits(id uint8, limitMbps, quotaGB float64) error {
	lg.mu.Lock()
	defer lg.mu.Unlock()
	r, err := lg.rowLocked(id)
	if err != nil {
		return err
	}
	r.cfg.LimitMbps, r.cfg.QuotaGB = limitMbps, quotaGB
	lg.be.limit(id, r.cfg.LimitKbps())
	used, _ := lg.usage.Get(r.cfg.Interface)
	lg.applyQuotaLocked(r, used)
	return nil
}

func (lg *ledger) ResetUsage(id uint8) error {
	lg.mu.Lock()
	defer lg.mu.Unlock()
	r, err := lg.rowLocked(id)
	if err != nil {
		return err
	}
	lg.usage.Reset(r.cfg.Interface)
	lg.applyQuotaLocked(r, 0)
	return lg.usage.Save()
}

func (lg *ledger) setWeightsLocked(id uint8, downMbps, upMbps float64) (*pathRow, error) {
	r, err := lg.rowLocked(id)
	if err != nil {
		return nil, err
	}
	// Weights only change after a speed test: remember that, so the path is
	// not measured again.
	r.cfg.DownMbps, r.cfg.UpMbps, r.cfg.MeasuredAt = downMbps, upMbps, time.Now().Format(time.RFC3339)
	return r, nil
}

func (lg *ledger) applyQuotaLocked(r *pathRow, used uint64) {
	q := r.cfg.QuotaBytes()
	over := q > 0 && used >= q
	switch {
	case over && !r.capped:
		r.capped = true
		lg.be.enable(r.id, false)
		log.Printf("path %s: data quota reached (%d MB), switched off", r.cfg.Name, used/1e6)
	case !over && r.capped:
		r.capped = false
		if !r.userOff {
			lg.be.enable(r.id, true)
		}
		log.Printf("path %s: under quota again, switched back on", r.cfg.Name)
	}
}

// account moves new traffic into the usage counters and enforces quotas.
func (lg *ledger) account() {
	totals := lg.be.totals()
	lg.mu.Lock()
	defer lg.mu.Unlock()
	for id, total := range totals {
		if int(id) >= len(lg.rows) {
			continue
		}
		r := lg.rows[id]
		if total < r.counted { // backend restarted its counters
			r.counted = 0
		}
		used := lg.usage.Add(r.cfg.Interface, total-r.counted)
		r.counted = total
		lg.applyQuotaLocked(r, used)
	}
}

// run accounts every second and persists usage periodically until ctx ends.
func (lg *ledger) run(ctx context.Context) {
	tick := time.NewTicker(usageTick)
	defer tick.Stop()
	lastSave := time.Now()
	for {
		select {
		case <-ctx.Done():
			lg.account()
			if err := lg.usage.Save(); err != nil {
				log.Printf("save usage: %v", err)
			}
			return
		case now := <-tick.C:
			lg.account()
			if now.Sub(lastSave) >= usageFlush {
				if err := lg.usage.Save(); err != nil {
					log.Printf("save usage: %v", err)
				}
				lastSave = now
			}
		}
	}
}

// snapshotLocked returns the user-state part of PathInfo for every path.
func (lg *ledger) snapshotLocked() []PathInfo {
	out := make([]PathInfo, 0, len(lg.rows))
	for _, r := range lg.rows {
		used, since := lg.usage.Get(r.cfg.Interface)
		out = append(out, PathInfo{ID: r.id, Path: r.cfg, Enabled: !r.userOff && !r.capped,
			Capped: r.capped, UsageBytes: used, UsageSince: since})
	}
	return out
}

func (lg *ledger) configsLocked(ids []uint8) []config.Path {
	out := make([]config.Path, len(ids))
	for i, id := range ids {
		if int(id) < len(lg.rows) {
			out[i] = lg.rows[id].cfg
		}
	}
	return out
}
