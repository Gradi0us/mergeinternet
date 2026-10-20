package engine

import (
	"context"
	"sync"
	"testing"
	"time"

	"mergeinternet/internal/sched"
)

// shaper drops datagrams above a byte rate (a token bucket with no queue), the
// way a policed or saturated link behaves.
type shaper struct {
	mu     sync.Mutex
	rate   float64 // bytes/s
	tokens float64
	max    float64
	last   time.Time
}

func newShaper(mbps float64) *shaper {
	rate := mbps * 1e6 / 8
	return &shaper{rate: rate, tokens: 16 << 10, max: 16 << 10, last: time.Now()}
}

func (s *shaper) allow(n int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	s.tokens += s.rate * now.Sub(s.last).Seconds()
	s.last = now
	if s.tokens > s.max {
		s.tokens = s.max
	}
	if s.tokens < float64(n) {
		return false
	}
	s.tokens -= float64(n)
	return true
}

// shapeLink limits path i to mbps in both directions.
func (h *harness) shapeLink(i int, mbps float64) {
	h.shape[i].Store(newShaper(mbps))
	h.shapeDown[i].Store(newShaper(mbps))
}

func within(got, want, tol float64) bool { return got >= want*(1-tol) && got <= want*(1+tol) }

func TestMeasureLinkSeesShapedRate(t *testing.T) {
	h := newHarness(t, 2, sched.Aggregate)
	h.shapeLink(1, 16)

	shaped, err := h.client.MeasureLink(h.ctx, 1, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	fast, err := h.client.MeasureLink(h.ctx, 0, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("shaped 16 Mbit/s link: down=%.1f up=%.1f; unshaped loopback link: down=%.1f up=%.1f",
		shaped.DownMbps, shaped.UpMbps, fast.DownMbps, fast.UpMbps)
	if !within(shaped.DownMbps, 16, 0.35) || !within(shaped.UpMbps, 16, 0.35) {
		t.Errorf("shaped link measured %+v, want 16 +/- 35%%", shaped)
	}
	if fast.DownMbps < 3*16 || fast.UpMbps < 3*16 {
		t.Errorf("unshaped link measured %+v, want > 48 Mbit/s each way", fast)
	}
}

func TestMeasureTwoLinksConcurrently(t *testing.T) {
	h := newHarness(t, 2, sched.Aggregate)
	h.shapeLink(0, 16)
	h.shapeLink(1, 40)
	var res [2]SpeedResult
	var errs [2]error
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res[i], errs[i] = h.client.MeasureLink(h.ctx, uint8(i), 2*time.Second)
		}()
	}
	wg.Wait()
	t.Logf("link0 %+v link1 %+v", res[0], res[1])
	for i, want := range []float64{16, 40} {
		if errs[i] != nil {
			t.Fatalf("link %d: %v", i, errs[i])
		}
		if !within(res[i].DownMbps, want, 0.35) || !within(res[i].UpMbps, want, 0.35) {
			t.Errorf("link %d measured %+v, want %v +/- 35%%", i, res[i], want)
		}
	}
}

func TestMeasureDeadLinkErrorsInTime(t *testing.T) {
	h := newHarness(t, 2, sched.Aggregate)
	h.cut[1].Store(true)
	const dur = time.Second
	start := time.Now()
	_, err := h.client.MeasureLink(h.ctx, 1, dur)
	if err == nil {
		t.Fatal("expected an error on a link that drops everything")
	}
	if el := time.Since(start); el > dur+3*time.Second {
		t.Fatalf("took %v, want <= %v", el, dur+3*time.Second)
	}
	t.Logf("dead link error after %v: %v", time.Since(start).Round(time.Millisecond), err)
}

func TestMeasureRespectsContext(t *testing.T) {
	h := newHarness(t, 2, sched.Aggregate)
	h.cut[0].Store(true)
	ctx, cancel := context.WithTimeout(h.ctx, 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := h.client.MeasureLink(ctx, 0, 2*time.Second); err == nil {
		t.Fatal("expected ctx error")
	}
	if el := time.Since(start); el > time.Second {
		t.Fatalf("ctx cancel took %v", el)
	}
}

func TestDataStaysOrderedDuringMeasurement(t *testing.T) {
	h := newHarness(t, 2, sched.Aggregate)
	h.client.speedCapMbps = 60 // keep loopback socket buffers from overflowing and eating data
	h.server.speedCapMbps = 60

	done := make(chan struct{})
	go func() {
		defer close(done)
		sendPaced(h.client, 0, 1500)
		sendPaced(h.server, 0, 1500)
	}()
	res, err := h.client.MeasureLink(h.ctx, 1, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("measured %+v while data flowed", res)
	<-done
	assertInOrder(t, waitCount(t, &h.toServer, 1500, h.server), 0)
	assertInOrder(t, waitCount(t, &h.toClient, 1500, h.client), 0)
	time.Sleep(100 * time.Millisecond)
	if n := len(h.toServer.snapshot()) + len(h.toClient.snapshot()); n != 3000 {
		t.Fatalf("delivered %d packets, want exactly 3000 (fill leaked into the TUN?)", n)
	}
}

func TestSetLinkWeightsReachesServer(t *testing.T) {
	h := newHarness(t, 2, sched.Aggregate)
	if h.client.SetLinkWeights(9, 1, 1) {
		t.Fatal("SetLinkWeights accepted an unknown link")
	}
	if h.server.SetLinkWeights(1, 1, 1) {
		t.Fatal("SetLinkWeights accepted on the server")
	}
	if !h.client.SetLinkWeights(1, 5000, 7777) {
		t.Fatal("SetLinkWeights failed on a known link")
	}
	if got := h.client.link(1).sendKbps.Load(); got != 5000 {
		t.Fatalf("client up weight = %d, want 5000", got)
	}
	deadline := time.Now().Add(3 * time.Second)
	for h.server.link(1).sendKbps.Load() != 7777 {
		if time.Now().After(deadline) {
			t.Fatalf("server never saw down weight 7777 (has %d)", h.server.link(1).sendKbps.Load())
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got := h.server.link(0).sendKbps.Load(); got != 10_000 {
		t.Fatalf("untouched link 0 weight = %d, want 10000", got)
	}
}

func TestMeasureUnknownLinkAndServerRole(t *testing.T) {
	h := newHarness(t, 1, sched.Aggregate)
	if _, err := h.client.MeasureLink(h.ctx, 7, time.Second); err == nil {
		t.Fatal("unknown link accepted")
	}
	if _, err := h.server.MeasureLink(h.ctx, 0, time.Second); err == nil {
		t.Fatal("server MeasureLink accepted")
	}
}

func TestServerFloodIsBoundedAndSingle(t *testing.T) {
	h := newHarness(t, 1, sched.Aggregate)
	l := h.server.link(0)
	// Absurd request: 1h at 60000 Mbit/s must be clamped to 5s / 2000 Mbit/s.
	h.server.startFlood(l, 11, time.Hour, 60000)
	h.server.startFlood(l, 11, time.Hour, 60000) // duplicate: ignored
	h.server.speed.mu.Lock()
	f := h.server.speed.floods[0]
	h.server.speed.mu.Unlock()
	if f == nil || f.testID != 11 {
		t.Fatalf("flood state %+v", f)
	}
	h.server.startFlood(l, 12, time.Hour, 60000) // replaces 11
	h.server.speed.mu.Lock()
	cur := h.server.speed.floods[0].testID
	h.server.speed.mu.Unlock()
	if cur != 12 {
		t.Fatalf("current flood = %d, want 12", cur)
	}
	if clampDur(time.Hour) != speedMaxDur || clampRate(60000) != speedMaxRate || clampRate(0) != speedDefaultRate {
		t.Fatal("clamps wrong")
	}
	h.server.speed.mu.Lock()
	h.server.speed.floods[0].cancel() // stop the test flood now
	h.server.speed.mu.Unlock()
}
