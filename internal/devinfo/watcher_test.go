package devinfo

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeEnv struct {
	mu       sync.Mutex
	raw      []rawAdapter
	enriches atomic.Int32
	fail     atomic.Bool
}

func (f *fakeEnv) cheap() ([]rawAdapter, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]rawAdapter(nil), f.raw...), nil
}

func (f *fakeEnv) enrich(context.Context) (map[int]extra, error) {
	f.enriches.Add(1)
	if f.fail.Load() {
		return nil, errors.New("boom")
	}
	return map[int]extra{5: {Index: 5, PnPID: `USB\VID_04E8&PID_1&MI_00\x`, WPDName: "Galaxy S24"}}, nil
}

func (f *fakeEnv) set(raw []rawAdapter) { f.mu.Lock(); f.raw = raw; f.mu.Unlock() }

func usbRaw(ip string) rawAdapter {
	return rawAdapter{Name: "Ethernet 3", Index: 5, Description: "Remote NDIS based Internet Sharing Device",
		Kind: "android", Up: true, IPv4: []string{ip}}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

func newFake(f *fakeEnv, d time.Duration) *Watcher {
	w := NewWatcher(d)
	w.cheap, w.enrich = f.cheap, f.enrich
	return w
}

func TestWatcherSnapshotCopyAndNilBeforeFirst(t *testing.T) {
	f := &fakeEnv{}
	f.set([]rawAdapter{usbRaw("192.168.42.2")})
	w := newFake(f, 20*time.Millisecond)
	if w.Snapshot() != nil {
		t.Fatal("snapshot must be nil before first result")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)
	waitFor(t, "first snapshot", func() bool { return w.Snapshot() != nil })
	s := w.Snapshot()
	s[0].Name = "MUTATED"
	s[0].IPv4[0] = "9.9.9.9"
	again := w.Snapshot()
	if again[0].Name == "MUTATED" || again[0].IPv4[0] == "9.9.9.9" {
		t.Fatal("Snapshot must return a copy")
	}
	waitFor(t, "enriched name", func() bool { s := w.Snapshot(); return len(s) > 0 && s[0].Name == "Galaxy S24" })
}

func TestWatcherEnrichmentOnlyOnChange(t *testing.T) {
	f := &fakeEnv{}
	f.set([]rawAdapter{usbRaw("192.168.42.2")})
	w := newFake(f, 10*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)
	waitFor(t, "first enrichment", func() bool { return f.enriches.Load() == 1 })
	time.Sleep(200 * time.Millisecond) // ~20 ticks, nothing changed
	if n := f.enriches.Load(); n != 1 {
		t.Fatalf("enrichment re-run without change: %d", n)
	}
	f.set([]rawAdapter{usbRaw("192.168.42.77")}) // IP changed -> fingerprint changed
	waitFor(t, "re-enrichment", func() bool { return f.enriches.Load() == 2 })
	time.Sleep(100 * time.Millisecond)
	if n := f.enriches.Load(); n != 2 {
		t.Fatalf("unexpected extra enrichment: %d", n)
	}
	s := w.Snapshot()
	if s[0].IPv4[0] != "192.168.42.77" {
		t.Errorf("snapshot not updated: %+v", s)
	}
	// adding an adapter also counts
	f.set([]rawAdapter{usbRaw("192.168.42.77"), {Name: "Wi-Fi", Index: 7, Kind: "wifi", Up: true, IPv4: []string{"10.0.0.2"}}})
	waitFor(t, "3rd enrichment", func() bool { return f.enriches.Load() == 3 })
}

func TestWatcherRefreshForces(t *testing.T) {
	f := &fakeEnv{}
	f.set([]rawAdapter{usbRaw("192.168.42.2")})
	w := newFake(f, time.Hour)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	devs := w.Refresh(ctx)
	if len(devs) != 1 || devs[0].Name != "Galaxy S24" || f.enriches.Load() != 1 {
		t.Fatalf("refresh: %+v enriches=%d", devs, f.enriches.Load())
	}
	w.Refresh(ctx)
	if f.enriches.Load() != 2 {
		t.Fatal("Refresh must always re-enrich")
	}
	if w.Snapshot()[0].Name != "Galaxy S24" {
		t.Fatal("snapshot not updated by Refresh")
	}
}

func TestWatcherEnrichFailureKeepsOldNamesAndRecovers(t *testing.T) {
	f := &fakeEnv{}
	f.set([]rawAdapter{usbRaw("192.168.42.2")})
	w := newFake(f, 10*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)
	waitFor(t, "named", func() bool { s := w.Snapshot(); return len(s) > 0 && s[0].Name == "Galaxy S24" })
	f.fail.Store(true)
	f.set([]rawAdapter{usbRaw("192.168.42.9")})
	waitFor(t, "failed attempt", func() bool { return f.enriches.Load() >= 2 })
	time.Sleep(50 * time.Millisecond)
	if s := w.Snapshot(); s[0].Name != "Galaxy S24" {
		t.Errorf("old name lost after failed enrichment: %+v", s)
	}
}

func TestDetectWith(t *testing.T) {
	f := &fakeEnv{}
	f.set([]rawAdapter{usbRaw("192.168.42.2")})
	devs, err := detectWith(context.Background(), f.cheap, f.enrich)
	if err != nil || len(devs) != 1 || devs[0].Via != ViaUSB || devs[0].Name != "Galaxy S24" {
		t.Fatalf("%v %+v", err, devs)
	}
}
