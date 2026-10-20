package localproxy

import (
	"sync"
	"time"
)

// limiter is a token bucket in bytes; rate 0 = unlimited. It allows debt so a
// chunk larger than the remaining tokens is delayed rather than split.
type limiter struct {
	mu     sync.Mutex
	rate   float64 // bytes per second
	burst  float64
	tokens float64
	last   time.Time
}

func (l *limiter) setMbps(mbps float64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.rate = mbps * 125000
	if l.rate <= 0 {
		l.rate = 0
		return
	}
	l.burst = l.rate / 10
	if l.burst < copyChunk {
		l.burst = copyChunk
	}
	l.tokens = l.burst
	l.last = time.Now()
}

// wait blocks until n bytes may pass, or stop is closed.
func (l *limiter) wait(n int, stop <-chan struct{}) {
	l.mu.Lock()
	if l.rate == 0 {
		l.mu.Unlock()
		return
	}
	now := time.Now()
	l.tokens += now.Sub(l.last).Seconds() * l.rate
	if l.tokens > l.burst {
		l.tokens = l.burst
	}
	l.last = now
	l.tokens -= float64(n)
	var d time.Duration
	if l.tokens < 0 {
		d = time.Duration(-l.tokens / l.rate * float64(time.Second))
	}
	l.mu.Unlock()
	if d <= 0 {
		return
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
	case <-stop:
	}
}
