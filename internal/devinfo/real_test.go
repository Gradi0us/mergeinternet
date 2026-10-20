package devinfo

import (
	"context"
	"encoding/json"
	"runtime"
	"testing"
	"time"
)

func TestRealDetect(t *testing.T) {
	if testing.Short() {
		t.Skip("real detection skipped in -short")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	start := time.Now()
	devs, err := Detect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("Detect took %v", time.Since(start))
	for _, d := range devs {
		b, _ := json.Marshal(d)
		t.Logf("%s", b)
	}
	if runtime.GOOS != "windows" {
		return
	}
	// Invariants that hold whatever is plugged in right now.
	if n := countRole(devs, RoleMain); n > 1 {
		t.Errorf("%d main lines, want at most 1", n)
	}
	lan := false
	for _, d := range devs {
		if d.Usable && d.Via == ViaLAN && d.Role == RoleMain {
			lan = true
		}
	}
	for _, d := range devs {
		if d.Kind != "wifi" || !d.Usable {
			continue
		}
		switch {
		case d.Role == RoleIgnore:
			t.Errorf("usable Wi-Fi %q ignored", d.Interface)
		case lan && d.Role == RoleMain:
			t.Errorf("Wi-Fi %q is main although LAN is up", d.Interface)
		}
		t.Logf("Wi-Fi %q: role=%s via=%s ssid=%q", d.Interface, d.Role, d.Via, d.SSID)
	}
}
