// Package sched decides which path(s) each outgoing packet is sent on.
package sched

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// minBucket is the smallest token-bucket capacity: big enough for a burst of
// full-size packets even at very low limits, so a cap never starves a path.
const minBucket = 32 << 10

type Mode int

const (
	// Aggregate sends each packet on exactly one path, proportional to path weight.
	Aggregate Mode = iota
	// Redundant sends every packet on every live path (lowest latency/loss, no extra speed).
	Redundant
)

func ParseMode(s string) (Mode, error) {
	switch s {
	case "", "aggregate":
		return Aggregate, nil
	case "redundant":
		return Redundant, nil
	}
	return 0, fmt.Errorf("unknown mode %q (want aggregate or redundant)", s)
}

func (m Mode) String() string {
	if m == Redundant {
		return "redundant"
	}
	return "aggregate"
}

type path struct {
	id     uint8
	weight float64
	alive  bool
	vt     float64 // virtual time: bytes sent / weight

	// Speed limit (token bucket). limitKbps == 0 means unlimited.
	limitKbps uint32
	rate      float64   // bytes per second
	capacity  float64   // max tokens (burst), bytes
	tokens    float64   // currently available bytes
	last      time.Time // last refill
}

// effWeight is the weight used for proportional splitting: a path capped below
// its nominal bandwidth must not be assigned more than the cap allows.
func (p *path) effWeight() float64 {
	if p.limitKbps != 0 && float64(p.limitKbps) < p.weight {
		return float64(p.limitKbps)
	}
	return p.weight
}

// refill adds the tokens earned since the last call (lazy, driven by Pick).
func (p *path) refill(now time.Time) {
	if p.limitKbps == 0 {
		return
	}
	if dt := now.Sub(p.last); dt > 0 {
		p.tokens += p.rate * dt.Seconds()
		if p.tokens > p.capacity {
			p.tokens = p.capacity
		}
	}
	p.last = now
}

// Scheduler is a smooth weighted fair scheduler: each packet goes to the live
// path whose virtual time is lowest, so a path with weight 3 carries ~3x the
// bytes of a path with weight 1.
type Scheduler struct {
	mu    sync.Mutex
	mode  Mode
	paths []*path

	now     func() time.Time // injectable clock for tests
	dropped atomic.Uint64
}

func New(mode Mode) *Scheduler { return &Scheduler{mode: mode, now: time.Now} }

// Dropped counts packets Pick refused because every live path was at its speed cap.
func (s *Scheduler) Dropped() uint64 { return s.dropped.Load() }

// SetLimit caps a path's send rate to kbps kbit/s (0 = unlimited) with a token
// bucket of max(100 ms of traffic, 32 KiB). The bucket starts full so a fresh
// cap does not stall the path. Setting an unchanged limit keeps the bucket.
func (s *Scheduler) SetLimit(id uint8, kbps uint32) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.find(id)
	if p == nil {
		// Limit arrived before the path was registered; Set() fills in the rest.
		p = &path{id: id, weight: 1}
		s.paths = append(s.paths, p)
	}
	if p.limitKbps == kbps {
		return
	}
	wasUnlimited := p.limitKbps == 0
	p.limitKbps = kbps
	if kbps == 0 {
		p.rate, p.capacity, p.tokens = 0, 0, 0
		return
	}
	p.rate = float64(kbps) * 1000 / 8
	p.capacity = p.rate * 0.1
	if p.capacity < minBucket {
		p.capacity = minBucket
	}
	if wasUnlimited || p.tokens > p.capacity {
		p.tokens = p.capacity // first cap, or the cap shrank
	}
	p.last = s.now()
}

// Set adds or updates a path. Weight is typically the path's bandwidth in kbit/s.
func (s *Scheduler) Set(id uint8, weight float64, alive bool) {
	if weight <= 0 {
		weight = 1
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.find(id)
	if p == nil {
		p = &path{id: id}
		s.paths = append(s.paths, p)
	}
	if alive && !p.alive {
		// A (re)joining path starts level with the others instead of
		// grabbing all traffic until its virtual time catches up.
		if min, ok := s.minAliveVT(); ok && p.vt < min {
			p.vt = min
		}
	}
	p.weight = weight
	p.alive = alive
}

// Pick appends the chosen path IDs for a packet of size bytes to dst.
// Paths whose speed-cap bucket cannot cover size are skipped; if live paths
// exist but none qualifies nothing is appended (the packet is dropped, like a
// policer, so TCP backs off). When no path is alive it still returns one,
// ignoring caps, so traffic keeps probing for recovery.
func (s *Scheduler) Pick(size int, dst []uint8) []uint8 {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.paths) == 0 {
		return dst
	}
	now := s.now()
	need := float64(size)
	alive := false
	var best *path
	n := len(dst)
	for _, p := range s.paths {
		if !p.alive {
			continue
		}
		alive = true
		p.refill(now)
		if p.limitKbps != 0 && p.tokens < need {
			continue
		}
		if s.mode == Redundant {
			dst = append(dst, p.id)
			if p.limitKbps != 0 {
				p.tokens -= need
			}
		} else if best == nil || p.vt < best.vt {
			best = p
		}
	}
	if alive {
		if s.mode == Aggregate && best != nil {
			best.vt += need / best.effWeight()
			if best.limitKbps != 0 {
				best.tokens -= need
			}
			dst = append(dst, best.id)
		}
		if len(dst) == n {
			s.dropped.Add(1)
		}
		return dst
	}
	for _, p := range s.paths {
		if best == nil || p.vt < best.vt {
			best = p
		}
	}
	best.vt += need / best.effWeight()
	return append(dst, best.id)
}

func (s *Scheduler) find(id uint8) *path {
	for _, p := range s.paths {
		if p.id == id {
			return p
		}
	}
	return nil
}

func (s *Scheduler) minAliveVT() (float64, bool) {
	var min float64
	found := false
	for _, p := range s.paths {
		if p.alive && (!found || p.vt < min) {
			min, found = p.vt, true
		}
	}
	return min, found
}
