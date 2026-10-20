package sched

import (
	"math"
	"slices"
	"testing"
	"time"
)

type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time { return c.t }

func newClocked(mode Mode) (*Scheduler, *fakeClock) {
	c := &fakeClock{t: time.Unix(1_000_000, 0)}
	s := New(mode)
	s.now = c.now
	return s, c
}

// run offers one size-byte packet every step for dur and returns bytes per path and packets dropped.
func run(s *Scheduler, c *fakeClock, dur, step time.Duration, size int) (map[uint8]int, int) {
	bytes := map[uint8]int{}
	drops := 0
	for end := c.t.Add(dur); c.t.Before(end); c.t = c.t.Add(step) {
		ids := s.Pick(size, nil)
		if len(ids) == 0 {
			drops++
		}
		for _, id := range ids {
			bytes[id] += size
		}
	}
	return bytes, drops
}

func share(s *Scheduler, n, size int) map[uint8]int {
	bytes := map[uint8]int{}
	for i := 0; i < n; i++ {
		for _, id := range s.Pick(size, nil) {
			bytes[id] += size
		}
	}
	return bytes
}

func TestAggregateSplitsByWeight(t *testing.T) {
	s := New(Aggregate)
	s.Set(0, 100_000, true) // fibre 100 Mbps
	s.Set(1, 40_000, true)  // 4G 40 Mbps
	s.Set(2, 30_000, true)  // 4G 30 Mbps
	got := share(s, 17_000, 1000)
	total := float64(17_000 * 1000)
	for id, w := range map[uint8]float64{0: 100, 1: 40, 2: 30} {
		want := w / 170
		if frac := float64(got[id]) / total; math.Abs(frac-want) > 0.01 {
			t.Errorf("path %d carried %.3f of bytes, want %.3f", id, frac, want)
		}
	}
}

func TestDeadPathGetsNoTraffic(t *testing.T) {
	s := New(Aggregate)
	s.Set(0, 1, true)
	s.Set(1, 1, false)
	if got := share(s, 100, 100); got[1] != 0 {
		t.Fatalf("dead path got %d bytes", got[1])
	}
}

func TestRevivedPathDoesNotHogTraffic(t *testing.T) {
	s := New(Aggregate)
	s.Set(0, 1, true)
	s.Set(1, 1, false)
	share(s, 1000, 100) // path 0 accumulates virtual time
	s.Set(1, 1, true)
	got := share(s, 100, 100)
	if got[0] < 4000 || got[1] < 4000 {
		t.Fatalf("revived path should share evenly, got %v", got)
	}
}

func TestNoAlivePathStillSends(t *testing.T) {
	s := New(Aggregate)
	s.Set(0, 1, false)
	s.Set(1, 1, false)
	if ids := s.Pick(100, nil); len(ids) != 1 {
		t.Fatalf("got %v, want one fallback path", ids)
	}
}

func TestRedundantUsesAllAlivePaths(t *testing.T) {
	s := New(Redundant)
	s.Set(0, 1, true)
	s.Set(1, 1, false)
	s.Set(2, 1, true)
	if ids := s.Pick(100, nil); !slices.Equal(ids, []uint8{0, 2}) {
		t.Fatalf("got %v", ids)
	}
}

func TestParseMode(t *testing.T) {
	if m, err := ParseMode(""); err != nil || m != Aggregate {
		t.Fatal("empty mode should default to aggregate")
	}
	if _, err := ParseMode("fast"); err == nil {
		t.Fatal("unknown mode accepted")
	}
}

func TestLimitedPathNeverExceedsRatePlusBurst(t *testing.T) {
	s, c := newClocked(Aggregate)
	s.Set(0, 10_000, true)
	s.Set(1, 10_000, true)
	s.SetLimit(0, 1000) // 125 kB/s
	// Offered load 2 MB/s for 10 s: far above the cap, so the cap must bind.
	got, drops := run(s, c, 10*time.Second, 500*time.Microsecond, 1000)
	max := 125_000.0*10 + minBucket
	if float64(got[0]) > max {
		t.Fatalf("limited path carried %d bytes, cap allows %.0f", got[0], max)
	}
	if float64(got[0]) < 0.95*125_000*10 {
		t.Fatalf("limited path underused: %d bytes", got[0])
	}
	if drops != 0 {
		t.Fatalf("unlimited path should absorb the rest, got %d drops", drops)
	}
	if total := got[0] + got[1]; total != 20_000*1000 {
		t.Fatalf("total %d bytes, want %d", total, 20_000*1000)
	}
	if s.Dropped() != 0 {
		t.Fatalf("Dropped()=%d", s.Dropped())
	}
}

func TestAllLimitedAndEmptyDropsPackets(t *testing.T) {
	s, c := newClocked(Aggregate)
	s.Set(0, 1000, true)
	s.Set(1, 1000, true)
	s.SetLimit(0, 800)
	s.SetLimit(1, 800)
	passed := 0
	for i := 0; i < 200; i++ { // 200 kB at one instant, buckets hold 2*32 KiB
		passed += len(s.Pick(1000, nil))
	}
	if passed < 60 || passed > 66 {
		t.Fatalf("passed %d packets, want ~65 (2 full buckets)", passed)
	}
	before := s.Dropped()
	if ids := s.Pick(1000, nil); len(ids) != 0 {
		t.Fatalf("got %v from empty buckets, want none", ids)
	}
	if s.Dropped() != before+1 {
		t.Fatalf("Dropped %d -> %d, want +1", before, s.Dropped())
	}
	if uint64(200-passed) != s.Dropped()-1 {
		t.Fatalf("dropped %d, want %d", s.Dropped()-1, 200-passed)
	}
	// Time passes: tokens come back and packets flow again.
	c.t = c.t.Add(time.Second)
	if ids := s.Pick(1000, nil); len(ids) != 1 {
		t.Fatalf("after refill got %v", ids)
	}
}

func TestLimitedAliveBeatsDeadFallback(t *testing.T) {
	s, _ := newClocked(Aggregate)
	s.Set(0, 1000, true)
	s.Set(1, 1000, false)
	s.SetLimit(0, 800)
	for i := 0; i < 100; i++ {
		s.Pick(1000, nil)
	}
	if ids := s.Pick(1000, nil); len(ids) != 0 {
		t.Fatalf("dead path must not rescue a capped live path, got %v", ids)
	}
	// With nothing alive the cap is ignored so recovery probing still works.
	s.Set(0, 1000, false)
	if ids := s.Pick(1000, nil); len(ids) != 1 {
		t.Fatalf("no-alive fallback gave %v", ids)
	}
}

func TestProportionalSplitUsesMinOfWeightAndLimit(t *testing.T) {
	s, c := newClocked(Aggregate)
	s.Set(0, 30_000, true) // nominal 30 Mbps but capped at 10
	s.Set(1, 10_000, true)
	s.SetLimit(0, 10_000)
	// 1 MB/s offered; each half (0.5 MB/s) stays under the 1.25 MB/s cap,
	// so only the weights decide the split.
	got, drops := run(s, c, 10*time.Second, time.Millisecond, 1000)
	if drops != 0 {
		t.Fatalf("%d drops", drops)
	}
	frac := float64(got[0]) / float64(got[0]+got[1])
	if math.Abs(frac-0.5) > 0.02 {
		t.Fatalf("capped path carried %.3f of bytes, want 0.5 (min(weight,limit)); raw weight would give 0.75", frac)
	}
}

func TestRedundantRespectsLimits(t *testing.T) {
	s, c := newClocked(Redundant)
	s.Set(0, 10_000, true)
	s.Set(1, 10_000, true)
	s.SetLimit(0, 1000)
	got, drops := run(s, c, 10*time.Second, time.Millisecond, 1000)
	if max := 125_000.0*10 + minBucket; float64(got[0]) > max {
		t.Fatalf("limited path carried %d, cap allows %.0f", got[0], max)
	}
	if got[0] < 1_000_000 {
		t.Fatalf("limited path underused: %d", got[0])
	}
	if got[1] != 10_000*1000 || drops != 0 {
		t.Fatalf("unlimited path got %d bytes, %d drops; want every packet", got[1], drops)
	}
	// Both capped and drained: nothing is sent.
	s.SetLimit(1, 1000)
	for i := 0; i < 100; i++ {
		s.Pick(1000, nil)
	}
	if ids := s.Pick(1000, nil); len(ids) != 0 {
		t.Fatalf("got %v with every path empty", ids)
	}
	if s.Dropped() == 0 {
		t.Fatal("drop not counted")
	}
}

func TestSetKeepsLimitAndBucket(t *testing.T) {
	s, _ := newClocked(Aggregate)
	s.Set(0, 1000, true)
	s.SetLimit(0, 800)
	for i := 0; i < 100; i++ { // drain the bucket
		s.Pick(1000, nil)
	}
	s.Set(0, 2000, true) // health tick / weight update must not refill or uncap
	passed := 0
	for i := 0; i < 100; i++ {
		passed += len(s.Pick(1000, nil))
	}
	if passed != 0 {
		t.Fatalf("Set() reset the limiter: %d packets passed", passed)
	}
	s.SetLimit(0, 0) // explicit removal works
	if ids := s.Pick(1000, nil); len(ids) != 1 {
		t.Fatalf("unlimited path got %v", ids)
	}
}
