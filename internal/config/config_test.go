package config

import (
	"fmt"
	"strings"
	"testing"
)

func clientJSON(paths string) []byte {
	return []byte(fmt.Sprintf(`{"server":"203.0.113.10:51820","key":"mGq3oR1s0Ff6Yb9w2kZ8cX4vN7tL5hP1aD3eQ0uJ6iM=","paths":[%s]}`, paths))
}

func path(name, iface, role string) string {
	r := ""
	if role != "" {
		r = fmt.Sprintf(`,"role":%q`, role)
	}
	return fmt.Sprintf(`{"name":%q,"interface":%q,"up_mbps":10,"down_mbps":40%s}`, name, iface, r)
}

func TestRolesDefaultForOlderConfigs(t *testing.T) {
	c, err := ParseClient(clientJSON(path("lan", "Ethernet", "") + "," + path("a", "Ethernet 3", "") + "," + path("b", "Ethernet 4", "")))
	if err != nil {
		t.Fatal(err)
	}
	got := []string{c.Paths[0].Role, c.Paths[1].Role, c.Paths[2].Role}
	if strings.Join(got, ",") != "main,extension,extension" {
		t.Fatalf("roles = %v", got)
	}
}

func TestExplicitMainNeedNotBeFirst(t *testing.T) {
	c, err := ParseClient(clientJSON(path("phone", "Ethernet 3", "") + "," + path("wifi", "Wi-Fi", "main")))
	if err != nil {
		t.Fatal(err)
	}
	if c.Paths[0].Role != RoleExtension || c.Paths[1].Role != RoleMain {
		t.Fatalf("roles = %s, %s", c.Paths[0].Role, c.Paths[1].Role)
	}
}

// extensions returns n extension paths on made-up interfaces.
func extensions(n int) string {
	var parts []string
	for i := 0; i < n; i++ {
		parts = append(parts, path(fmt.Sprintf("phone %d", i+1), fmt.Sprintf("Ethernet %d", i+10), "extension"))
	}
	return strings.Join(parts, ",")
}

// The supported setups: LAN + Wi-Fi hotspot + USB, LAN + 2 USB, Wi-Fi + 2 USB,
// and the main line with the maximum number of extension devices.
func TestSupportedTopologies(t *testing.T) {
	for name, paths := range map[string]string{
		"lan+hotspot+usb": path("LAN", "Ethernet", "main") + "," + path("iPhone", "Wi-Fi", "extension") + "," + path("vivo", "Ethernet 3", "extension"),
		"lan+usb+usb":     path("LAN", "Ethernet", "main") + "," + path("iPhone", "Ethernet 4", "extension") + "," + path("vivo", "Ethernet 3", "extension"),
		"wifi+usb+usb":    path("Home", "Wi-Fi", "main") + "," + path("iPhone", "Ethernet 4", "extension") + "," + path("vivo", "Ethernet 3", "extension"),
		"main+max":        path("LAN", "Ethernet", "main") + "," + extensions(MaxExtensions),
	} {
		if _, err := ParseClient(clientJSON(paths)); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestRoleLimits(t *testing.T) {
	cases := map[string]string{
		"two mains":           path("a", "A", "main") + "," + path("b", "B", "main"),
		"too many extensions": path("m", "M", "main") + "," + extensions(MaxExtensions+1),
		"unknown role":        path("a", "A", "backup"),
		"negative limit":      `{"name":"a","interface":"A","up_mbps":1,"down_mbps":1,"limit_mbps":-1}`,
	}
	for name, paths := range cases {
		if _, err := ParseClient(clientJSON(paths)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestLocalModeNeedsNoServer(t *testing.T) {
	c, err := ParseClient([]byte(`{"paths":[` + path("Wi-Fi", "Wi-Fi", "main") + `]}`))
	if err != nil {
		t.Fatal(err)
	}
	if c.Connection != ConnLocal || c.HasServer() {
		t.Fatalf("connection=%q hasServer=%v", c.Connection, c.HasServer())
	}
	if _, err := ParseClient([]byte(`{"connection":"vps","paths":[` + path("Wi-Fi", "Wi-Fi", "main") + `]}`)); err == nil {
		t.Fatal("vps mode without a server accepted")
	}
	// Older configs (server set, no "connection") keep using the VPS.
	c, err = ParseClient(clientJSON(path("Wi-Fi", "Wi-Fi", "main")))
	if err != nil || c.Connection != ConnVPS || !c.HasServer() {
		t.Fatalf("old config: %v %+v", err, c)
	}
	// Local mode may keep a valid server for later, but not a broken one.
	if _, err := ParseClient([]byte(`{"connection":"local","server":"x","paths":[` + path("Wi-Fi", "Wi-Fi", "main") + `]}`)); err == nil {
		t.Fatal("broken server accepted in local mode")
	}
}

func TestDraftWithoutPathsSavesAndLoads(t *testing.T) {
	path := t.TempDir() + "/client.json"
	draft := &Client{Server: "203.0.113.10:51820", Key: "mGq3oR1s0Ff6Yb9w2kZ8cX4vN7tL5hP1aD3eQ0uJ6iM="}
	if err := SaveClient(path, draft); err != nil {
		t.Fatalf("draft rejected: %v", err)
	}
	if _, err := LoadClient(path); err == nil {
		t.Fatal("a draft must not count as a runnable config")
	}
	c, err := LoadDraft(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.ServerAddr.String() != "203.0.113.10:51820" || len(c.KeyBytes) != 32 || len(c.Paths) != 0 || c.Tun.MTU != DefaultMTU {
		t.Fatalf("draft = %+v", c)
	}
	if err := SaveClient(path, &Client{Server: "nope", Key: draft.Key}); err == nil {
		t.Fatal("a draft still needs a valid server")
	}
}

func TestLimitAndQuotaUnits(t *testing.T) {
	p := Path{LimitMbps: 2.5, QuotaGB: 1.5}
	if p.LimitKbps() != 2500 || p.QuotaBytes() != 1_500_000_000 {
		t.Fatalf("got %d kbps, %d bytes", p.LimitKbps(), p.QuotaBytes())
	}
}
