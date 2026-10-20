package devinfo

import (
	"context"
	"sync"
	"time"
)

// Watcher keeps a fresh device list: a cheap pass every interval and the slow
// enrichment only when the network layout changed (or on Refresh / every 2 min).
type Watcher struct {
	interval time.Duration
	cheap    cheapFn
	enrich   enrichFn

	mu        sync.Mutex
	snapshot  []Device
	lastRaw   []rawAdapter
	extras    map[int]extra
	extraFP   string
	extraAt   time.Time
	lastTry   time.Time
	failed    bool
	enriching bool
}

func NewWatcher(interval time.Duration) *Watcher {
	if interval <= 0 {
		interval = 3 * time.Second
	}
	return &Watcher{interval: interval, cheap: cheapScan, enrich: enrichScan}
}

// Run refreshes immediately, then every interval, until ctx is done.
func (w *Watcher) Run(ctx context.Context) {
	w.tick(ctx)
	t := time.NewTicker(w.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			w.tick(ctx)
		}
	}
}

// Snapshot returns a copy of the latest result (nil before the first one).
func (w *Watcher) Snapshot() []Device {
	w.mu.Lock()
	defer w.mu.Unlock()
	return copyDevices(w.snapshot)
}

func copyDevices(in []Device) []Device {
	if in == nil {
		return nil
	}
	out := make([]Device, len(in))
	for i, d := range in {
		d.IPv4 = append([]string{}, d.IPv4...)
		out[i] = d
	}
	return out
}

func (w *Watcher) publishLocked() {
	w.snapshot = classify(merge(w.lastRaw, w.extras))
	if w.snapshot == nil {
		w.snapshot = []Device{}
	}
}

func (w *Watcher) tick(ctx context.Context) {
	raw, err := w.cheap()
	if err != nil {
		return
	}
	fp := fingerprint(raw)
	w.mu.Lock()
	defer w.mu.Unlock()
	w.lastRaw = raw
	w.publishLocked()
	now := time.Now()
	stale := fp != w.extraFP || now.Sub(w.extraAt) > enrichMaxAge
	if !stale || w.enriching || (w.failed && now.Sub(w.lastTry) < enrichBackoff) {
		return
	}
	w.enriching = true
	w.lastTry = now
	go w.runEnrich(ctx, fp)
}

func (w *Watcher) runEnrich(ctx context.Context, fp string) {
	ex, err := w.enrich(ctx)
	w.mu.Lock()
	defer w.mu.Unlock()
	w.enriching = false
	if err != nil || ex == nil {
		w.failed = true // keep previous names; retried after backoff
		return
	}
	w.failed = false
	w.extras, w.extraFP, w.extraAt = ex, fp, time.Now()
	w.publishLocked()
}

// Refresh forces a full refresh now and returns the result.
func (w *Watcher) Refresh(ctx context.Context) []Device {
	raw, err := w.cheap()
	if err != nil {
		return w.Snapshot()
	}
	fp := fingerprint(raw)
	ex, eerr := w.enrich(ctx)
	w.mu.Lock()
	defer w.mu.Unlock()
	w.lastRaw = raw
	if eerr == nil && ex != nil {
		w.extras, w.extraFP, w.extraAt = ex, fp, time.Now()
		w.failed = false
	}
	w.publishLocked()
	return copyDevices(w.snapshot)
}
