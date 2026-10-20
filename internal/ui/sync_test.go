package ui

import (
	"context"
	"sync"
	"testing"
	"time"

	"mergeinternet/internal/client"
	"mergeinternet/internal/config"
)

// fakeRunner records hot-added paths; everything else is a no-op.
type fakeRunner struct {
	mu    sync.Mutex
	paths []client.PathInfo
	done  chan struct{}
}

func (f *fakeRunner) Mode() string { return config.ConnLocal }
func (f *fakeRunner) Paths() []client.PathInfo {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]client.PathInfo(nil), f.paths...)
}
func (f *fakeRunner) AddPath(p config.Path) (uint8, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := uint8(len(f.paths))
	f.paths = append(f.paths, client.PathInfo{ID: id, Path: p, Enabled: true})
	return id, nil
}
func (f *fakeRunner) SetEnabled(uint8, bool) error             { return nil }
func (f *fakeRunner) SetLimits(uint8, float64, float64) error  { return nil }
func (f *fakeRunner) SetWeights(uint8, float64, float64) error { return nil }
func (f *fakeRunner) ResetUsage(uint8) error                   { return nil }
func (f *fakeRunner) StartedAt() time.Time                     { return time.Time{} }
func (f *fakeRunner) Stop()                                    {}
func (f *fakeRunner) Done() <-chan struct{}                    { return f.done }
func (f *fakeRunner) Err() error                               { return nil }
func (f *fakeRunner) Measure(context.Context, []uint8, time.Duration, func(client.MeasureResult)) []client.MeasureResult {
	return nil
}

// A phone saved to the config while the connection is still starting must
// reach the connection once it is up, and only once.
func TestPathSavedWhileStartingIsHotAdded(t *testing.T) {
	s, _ := testServer(t)
	wifi := config.Path{Name: "Wi-Fi", Interface: "Wi-Fi", Role: config.RoleMain, DownMbps: 100, UpMbps: 30}
	phone := config.Path{Name: "vivo", Interface: "Ethernet 7", Role: config.RoleExtension, DownMbps: 30, UpMbps: 8}
	if err := config.SaveClient(s.opts.ConfigPath, &config.Client{Connection: config.ConnLocal, Paths: []config.Path{wifi}}); err != nil {
		t.Fatal(err)
	}

	// The connection was started from the config above...
	r := &fakeRunner{done: make(chan struct{})}
	r.AddPath(wifi)
	// ...and the phone was auto-added before s.runner was set (live=false).
	if live, _, err := s.addPath(phone); err != nil || live {
		t.Fatalf("addPath while starting: live=%v err=%v", live, err)
	}

	s.syncPaths(r)
	s.syncPaths(r) // idempotent
	if id, err := s.hotAdd(r, phone); err != nil || id != 1 {
		t.Fatalf("hotAdd existing: id=%d err=%v", id, err)
	}
	got := r.Paths()
	if len(got) != 2 || got[1].Path.Interface != "Ethernet 7" {
		t.Fatalf("runner paths = %+v, want Wi-Fi + Ethernet 7", got)
	}
}

// LAN + the same router's Wi-Fi share one public IP: the Wi-Fi is flagged.
// Phones are never flagged (carriers share public IPs between subscribers).
func TestSharedUplinkFlagsOnlyLANAndWiFi(t *testing.T) {
	cfg := &config.Client{Paths: []config.Path{
		{Name: "LAN", Interface: "Ethernet", Role: config.RoleMain, PublicIP: "118.68.159.117"},
		{Name: "Wi-Fi", Interface: "Wi-Fi", Role: config.RoleExtension, PublicIP: "118.68.159.117"},
		{Name: "Viettel", Interface: "Wi-Fi 2", Role: config.RoleExtension, PublicIP: "27.72.1.2"},
		{Name: "vivo", Interface: "Ethernet 7", Role: config.RoleExtension, PublicIP: "118.68.159.117"},
	}}
	links := []linkView{
		{Iface: "Ethernet", Role: config.RoleMain, Device: "Mạng LAN", Via: "lan"},
		{Iface: "Wi-Fi", Role: config.RoleExtension, Device: "Minhhoangg_5G", Via: "wifi"},
		{Iface: "Wi-Fi 2", Role: config.RoleExtension, Device: "Viettel_5G", Via: "wifi"},
		{Iface: "Ethernet 7", Role: config.RoleExtension, Device: "vivo X300 Pro", Via: "usb"},
	}
	markSharedUplinks(links, cfg)
	want := []string{"", "Mạng LAN", "", ""}
	for i, l := range links {
		if l.SameNetAs != want[i] {
			t.Errorf("%s: sameNetAs=%q, want %q", l.Iface, l.SameNetAs, want[i])
		}
	}
}
