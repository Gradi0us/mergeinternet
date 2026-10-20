package client

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"

	"mergeinternet/internal/config"
	"mergeinternet/internal/engine"
)

// MeasureDuration is how long each direction of a link is tested.
const MeasureDuration = 3 * time.Second

// headroom keeps weights a little under the measured peak: 4G capacity moves
// around, and an over-weighted link builds queues and reorder stalls.
const headroom = 0.9

// MeasureResult is the measured capacity of one path.
type MeasureResult struct {
	Index     int
	Interface string
	DownMbps  float64
	UpMbps    float64
	Err       error
}

// Weights converts a measurement into scheduler weights (Mbps).
func (r MeasureResult) Weights() (down, up float64) {
	return max(1, round1(r.DownMbps*headroom)), max(1, round1(r.UpMbps*headroom))
}

func round1(v float64) float64 { return float64(int64(v*10+0.5)) / 10 }

// MeasureVPS tests every path in paths against the VPS while the tunnel is OFF:
// it opens one pinned socket per interface and talks to the server directly,
// without touching the machine's routes. Paths are measured concurrently,
// which is also how they will be used (the VPS's own bandwidth is shared).
// progress is called whenever a path finishes; it may be nil.
func MeasureVPS(ctx context.Context, cfg *config.Client, paths []config.Path, dur time.Duration, progress func(MeasureResult)) []MeasureResult {
	if !cfg.HasServer() {
		return failAll(paths, errors.New("chưa có máy chủ VPS"), progress)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	eng, err := engine.NewClient(engine.Config{
		Key:          cfg.KeyBytes,
		Mode:         cfg.SchedMode,
		ReorderDelay: cfg.Reorder,
		Deliver:      func([]byte) error { return nil }, // nothing is tunnelled during a test
		Logf:         log.Printf,
	})
	if err != nil {
		return failAll(paths, err, progress)
	}
	var wg sync.WaitGroup
	defer wg.Wait()
	for i, p := range paths {
		pc := &pathConn{path: p, server: cfg.ServerAddr, eng: eng}
		down, up := config.DefaultSpeeds(p.Role)
		pc.link = eng.AddLink(uint8(i), p.Name, kbps(up), kbps(down), pc.send)
		wg.Add(1)
		go func() { defer wg.Done(); pc.supervise(ctx) }()
	}
	wg.Add(1)
	go func() { defer wg.Done(); eng.Run(ctx) }()

	ids := make([]uint8, len(paths))
	for i := range paths {
		ids[i] = uint8(i)
	}
	return measureLinks(ctx, eng, ids, paths, dur, progress)
}

var errNoReply = errors.New("VPS không trả lời qua đường này (kiểm tra kết nối của thiết bị, hoặc cổng UDP trên VPS bị chặn)")

func measureLinks(ctx context.Context, eng *engine.Engine, ids []uint8, paths []config.Path, dur time.Duration, progress func(MeasureResult)) []MeasureResult {
	out := make([]MeasureResult, len(ids))
	var wg sync.WaitGroup
	var mu sync.Mutex
	for i, id := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := MeasureResult{Index: int(id), Interface: paths[i].Interface}
			if err := waitAlive(ctx, eng, id, 10*time.Second); err != nil {
				r.Err = err
			} else {
				res, err := eng.MeasureLink(ctx, id, dur)
				r.DownMbps, r.UpMbps, r.Err = res.DownMbps, res.UpMbps, err
			}
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

// waitAlive waits for the first probe round trip on a link.
func waitAlive(ctx context.Context, eng *engine.Engine, id uint8, timeout time.Duration) error {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		links, _, _ := eng.Stats()
		for _, l := range links {
			if l.ID == id && l.Alive {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return errNoReply
		case <-tick.C:
		}
	}
}

func failAll(paths []config.Path, err error, progress func(MeasureResult)) []MeasureResult {
	out := make([]MeasureResult, len(paths))
	for i, p := range paths {
		out[i] = MeasureResult{Index: i, Interface: p.Interface, Err: err}
		if progress != nil {
			progress(out[i])
		}
	}
	return out
}
