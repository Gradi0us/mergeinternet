package ui

import (
	"time"

	"mergeinternet/internal/client"
)

const historyLen = 120 // seconds shown in the chart

// Sample is one second of per-path throughput, indexed by path ID.
type Sample struct {
	T  int64     `json:"t"` // unix ms
	Rx []float64 `json:"rx"`
	Tx []float64 `json:"tx"`
}

type linkRate struct {
	rx, tx float64
}

type counters struct{ rx, tx uint64 }

// sampler turns cumulative byte counters into per-second rates and a short history.
type sampler struct {
	prev   map[uint8]counters
	prevAt time.Time
	rates  map[uint8]linkRate
	hist   []Sample
}

func (s *sampler) reset() { *s = sampler{} }

func (s *sampler) add(now time.Time, paths []client.PathInfo) {
	cur := make(map[uint8]counters, len(paths))
	n := 0
	for _, p := range paths {
		cur[p.ID] = counters{p.RxBytes, p.TxBytes}
		n = max(n, int(p.ID)+1)
	}
	if s.prev != nil {
		dt := now.Sub(s.prevAt).Seconds()
		if dt <= 0 {
			return
		}
		smp := Sample{T: now.UnixMilli(), Rx: make([]float64, n), Tx: make([]float64, n)}
		s.rates = make(map[uint8]linkRate, len(cur))
		for id, c := range cur {
			p, ok := s.prev[id]
			if !ok || c.rx < p.rx || c.tx < p.tx {
				continue // new path (hot-added) or counters restarted
			}
			rx := float64(c.rx-p.rx) * 8 / dt / 1e6
			tx := float64(c.tx-p.tx) * 8 / dt / 1e6
			s.rates[id] = linkRate{rx: rx, tx: tx}
			smp.Rx[id], smp.Tx[id] = round2(rx), round2(tx)
		}
		s.hist = append(s.hist, smp)
		if over := len(s.hist) - historyLen; over > 0 {
			s.hist = append(s.hist[:0:0], s.hist[over:]...)
		}
	}
	s.prev = cur
	s.prevAt = now
}

func (s *sampler) rate(id uint8) linkRate { return s.rates[id] }

func round2(v float64) float64 { return float64(int64(v*100+0.5)) / 100 }
