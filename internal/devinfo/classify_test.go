package devinfo

import (
	"fmt"
	"testing"
)

func byIface(t *testing.T, devs []Device, name string) Device {
	t.Helper()
	for _, d := range devs {
		if d.Interface == name {
			return d
		}
	}
	t.Fatalf("interface %q not in result", name)
	return Device{}
}

func expect(t *testing.T, d Device, role Role, via Via, name, vendor string) {
	t.Helper()
	if d.Role != role || d.Via != via || d.Name != name || d.Vendor != vendor {
		t.Errorf("%s: got role=%s via=%s name=%q vendor=%q; want %s %s %q %q",
			d.Interface, d.Role, d.Via, d.Name, d.Vendor, role, via, name, vendor)
	}
}

var (
	rEth = rawAdapter{Name: "Ethernet", Index: 5, Description: "Realtek PCIe GbE Family Controller", Kind: "ethernet",
		Up: true, IPv4: []string{"192.168.1.20"}, Gateway: "192.168.1.1", PnPID: `PCI\VEN_10EC&DEV_8168\4&1`}
	rForti = rawAdapter{Name: "Fortinet SSL VPN", Index: 9, Description: "Fortinet Virtual Ethernet Adapter", Kind: "ethernet",
		Up: true, IPv4: []string{"10.212.134.200"}, PnPID: `ROOT\NET\0000`}
)

func wifi(profile, gw string, ip string) rawAdapter {
	return rawAdapter{Name: "Wi-Fi", Index: 12, Description: "Intel(R) Wi-Fi 6E AX210 160MHz", Kind: "wifi", Up: ip != "",
		IPv4: nonEmpty(ip), Gateway: gw, PnPID: `PCI\VEN_8086&DEV_2725\3&1`, Profile: profile}
}

func nonEmpty(s string) []string {
	if s == "" {
		return nil
	}
	return []string{s}
}

func TestTopology1EthernetHotspotUSB(t *testing.T) {
	in := []rawAdapter{
		rEth, rForti,
		wifi("Minh's iPhone", "172.20.10.1", "172.20.10.4"),
		{Name: "Ethernet 3", Index: 20, Description: "Remote NDIS based Internet Sharing Device", Kind: "android", Up: true,
			IPv4: []string{"192.168.42.129"}, Gateway: "192.168.42.129", PnPID: `USB\VID_2D95&PID_6002&MI_00\7&1`,
			WPDName: "vivo X300 Pro", ParentDesc: "USB Composite Device"},
	}
	devs := classify(in)
	expect(t, byIface(t, devs, "Ethernet"), RoleMain, ViaLAN, "Mạng LAN", "")
	expect(t, byIface(t, devs, "Wi-Fi"), RoleExtension, ViaHotspot, "Minh's iPhone", "Apple")
	expect(t, byIface(t, devs, "Ethernet 3"), RoleExtension, ViaUSB, "vivo X300 Pro", "vivo")
	expect(t, byIface(t, devs, "Fortinet SSL VPN"), RoleIgnore, ViaLAN, "Fortinet Virtual Ethernet Adapter", "")
	if devs[0].Role != RoleMain || devs[len(devs)-1].Role != RoleIgnore {
		t.Errorf("bad sort order: %+v", devs)
	}
	if g := byIface(t, devs, "Wi-Fi").Gateway; g != "172.20.10.1" {
		t.Errorf("gateway = %q", g)
	}
}

func TestTopology2EthernetTwoUSB(t *testing.T) {
	in := []rawAdapter{
		rEth,
		{Name: "Ethernet 2", Index: 21, Description: "Apple Mobile Device Ethernet", Kind: "iphone", Up: true,
			IPv4: []string{"172.20.10.2"}, Gateway: "172.20.10.1", PnPID: `USB\VID_05AC&PID_12A8&MI_00\8&2`,
			WPDName: "Apple iPhone", ParentDesc: "Apple Mobile Device USB Composite Device"},
		{Name: "Ethernet 3", Index: 22, Description: "SAMSUNG Mobile USB Remote NDIS Network Device", Kind: "android", Up: true,
			IPv4: []string{"192.168.42.100"}, PnPID: `USB\VID_04E8&PID_6863&MI_00\9&3`,
			ParentDesc: "Galaxy S24", ParentName: "USB Composite Device"},
	}
	devs := classify(in)
	expect(t, byIface(t, devs, "Ethernet"), RoleMain, ViaLAN, "Mạng LAN", "")
	expect(t, byIface(t, devs, "Ethernet 2"), RoleExtension, ViaUSB, "Apple iPhone", "Apple")
	expect(t, byIface(t, devs, "Ethernet 3"), RoleExtension, ViaUSB, "Galaxy S24", "Samsung")
}

func TestTopology3HomeWiFiTwoUSB(t *testing.T) {
	in := []rawAdapter{
		wifi("Nha Minh 5G", "192.168.1.1", "192.168.1.30"),
		{Name: "Ethernet", Index: 5, Description: "Realtek PCIe GbE Family Controller", Kind: "ethernet", Up: false,
			PnPID: `PCI\VEN_10EC&DEV_8168\4&1`},
		{Name: "Ethernet 2", Index: 21, Description: "Apple Mobile Device Ethernet", Kind: "iphone", Up: true,
			IPv4: []string{"172.20.10.2"}, PnPID: `USB\VID_05AC&PID_12A8&MI_00\8&2`, WPDName: "Apple iPhone"},
		{Name: "Ethernet 3", Index: 22, Description: "Remote NDIS based Internet Sharing Device", Kind: "android", Up: true,
			IPv4: []string{"192.168.42.100"}, PnPID: `USB\VID_04E8&PID_6863&MI_00\9&3`, ParentDesc: "Galaxy S24"},
	}
	devs := classify(in)
	expect(t, byIface(t, devs, "Wi-Fi"), RoleMain, ViaWiFi, "Nha Minh 5G", "")
	expect(t, byIface(t, devs, "Ethernet 2"), RoleExtension, ViaUSB, "Apple iPhone", "Apple")
	expect(t, byIface(t, devs, "Ethernet 3"), RoleExtension, ViaUSB, "Galaxy S24", "Samsung")
	eth := byIface(t, devs, "Ethernet")
	if eth.Usable || eth.Role == RoleMain {
		t.Errorf("unplugged ethernet must not be main/usable: %+v", eth)
	}
	if n := countRole(devs, RoleMain); n != 1 {
		t.Errorf("main count = %d", n)
	}
}

func countRole(devs []Device, r Role) int {
	n := 0
	for _, d := range devs {
		if d.Role == r {
			n++
		}
	}
	return n
}

// LAN + a Wi-Fi network: LAN stays the main line and the Wi-Fi becomes an
// extra network (not a phone hotspot) unless it looks like a phone's hotspot.
// The 4th case: PC on LAN + another Wi-Fi + 3 phones over USB.
func TestEthernetPlusWiFiIsExtraNetwork(t *testing.T) {
	devs := classify([]rawAdapter{rEth, wifi("AndroidAP_1234", "192.168.43.1", "192.168.43.50")})
	expect(t, byIface(t, devs, "Wi-Fi"), RoleExtension, ViaHotspot, "AndroidAP_1234", "")
	devs = classify([]rawAdapter{rEth, wifi("Cafe Free WiFi", "10.0.0.1", "10.0.0.5")})
	expect(t, byIface(t, devs, "Ethernet"), RoleMain, ViaLAN, "Mạng LAN", "")
	expect(t, byIface(t, devs, "Wi-Fi"), RoleExtension, ViaWiFi, "Cafe Free WiFi", "")

	in := []rawAdapter{rEth, wifi("Viettel_5G", "192.168.0.1", "192.168.0.23")}
	for i, name := range []string{"Ethernet 7", "Ethernet 8", "Ethernet 9"} {
		r := rawAdapter{Description: "Remote NDIS based Internet Sharing Device", Kind: "android", Up: true, PnPID: `USB\VID_2D95&PID_600A\X`}
		r.Name, r.Index, r.IPv4 = name, 40+i, []string{fmt.Sprintf("10.1.%d.2", i)}
		in = append(in, r)
	}
	devs = classify(in)
	if countRole(devs, RoleMain) != 1 || countRole(devs, RoleExtension) != 4 {
		t.Fatalf("LAN + Wi-Fi + 3 USB: main=%d extensions=%d", countRole(devs, RoleMain), countRole(devs, RoleExtension))
	}
	expect(t, byIface(t, devs, "Wi-Fi"), RoleExtension, ViaWiFi, "Viettel_5G", "")
	for _, name := range []string{"Ethernet 7", "Ethernet 8", "Ethernet 9"} {
		if d := byIface(t, devs, name); d.Role != RoleExtension || d.Via != ViaUSB {
			t.Errorf("%s: role=%s via=%s", name, d.Role, d.Via)
		}
	}
}

func TestHotspotWithoutEthernetByGatewayOrKeyword(t *testing.T) {
	devs := classify([]rawAdapter{wifi("whatever", "172.20.10.1", "172.20.10.3")})
	expect(t, devs[0], RoleExtension, ViaHotspot, "whatever", "")
	devs = classify([]rawAdapter{wifi("Galaxy A54 1234", "192.168.43.1", "192.168.43.3")})
	expect(t, devs[0], RoleExtension, ViaHotspot, "Galaxy A54 1234", "Samsung")
}

func TestVirtualIgnored(t *testing.T) {
	devs := classify([]rawAdapter{rForti, {Name: "vEthernet", Index: 3, Kind: "virtual", Up: true, IPv4: []string{"172.30.0.1"}},
		{Name: "X", Index: 4, Kind: "ethernet", Up: true, IPv4: []string{"10.0.0.2"}, PnPID: `SWD\MSRRAS\X`}})
	for _, d := range devs {
		if d.Role != RoleIgnore {
			t.Errorf("%s should be ignore, got %s", d.Interface, d.Role)
		}
	}
}

func TestGenericNamesRejected(t *testing.T) {
	for _, s := range []string{"", "USB Composite Device", "Network Controller", "Remote NDIS based Internet Sharing Device",
		"Apple Mobile Device Ethernet", "MTP USB Device", "MTP", "Android", "android", "Generic USB Hub", "Composite"} {
		if !isGeneric(s) {
			t.Errorf("%q should be generic", s)
		}
	}
	for _, s := range []string{"Galaxy S24", "vivo X300 Pro", "Android Phone X", "iPhone của Minh"} {
		if isGeneric(s) {
			t.Errorf("%q should not be generic", s)
		}
	}
	// all generic -> vendor fallback, then description
	r := rawAdapter{Name: "E", Kind: "android", Up: true, IPv4: []string{"192.168.42.2"},
		PnPID: `USB\VID_18D1&PID_4EE3&MI_00\1`, Description: "Remote NDIS based Internet Sharing Device",
		WPDName: "MTP", ParentDesc: "USB Composite Device", ParentName: "Android"}
	d := classify([]rawAdapter{r})[0]
	expect(t, d, RoleExtension, ViaUSB, "Điện thoại Google", "Google")
	r.PnPID = `USB\VID_05AC&PID_1&MI_00\1`
	expect(t, classify([]rawAdapter{r})[0], RoleExtension, ViaUSB, "iPhone", "Apple")
	r.PnPID = `USB\VID_FFFF&PID_1&MI_00\1`
	expect(t, classify([]rawAdapter{r})[0], RoleExtension, ViaUSB, "Remote NDIS based Internet Sharing Device", "")
}

func TestNonUsableUSBStillExtension(t *testing.T) {
	r := rawAdapter{Name: "Ethernet 3", Index: 22, Description: "Remote NDIS based Internet Sharing Device", Kind: "android",
		Up: true, IPv4: []string{"169.254.10.5"}, PnPID: `USB\VID_04E8&PID_6863&MI_00\9`, ParentDesc: "Galaxy S24"}
	d := classify([]rawAdapter{r})[0]
	if d.Role != RoleExtension || d.Via != ViaUSB || d.Usable || len(d.IPv4) != 0 {
		t.Errorf("got %+v", d)
	}
	// USB detected by PnP prefix + NDIS description even if bindif kind says ethernet
	r.Kind = "ethernet"
	if d := classify([]rawAdapter{r})[0]; d.Via != ViaUSB {
		t.Errorf("pnp-based usb detection failed: %+v", d)
	}
}

func TestExtraEthernetBecomesExtensionLAN(t *testing.T) {
	e2 := rEth
	e2.Name, e2.Index = "Ethernet 9", 30
	devs := classify([]rawAdapter{rEth, e2})
	if countRole(devs, RoleMain) != 1 {
		t.Fatalf("want exactly one main: %+v", devs)
	}
	d := byIface(t, devs, "Ethernet 9")
	if d.Role != RoleExtension || d.Via != ViaLAN {
		t.Errorf("got %+v", d)
	}
}

func TestParseEnrichment(t *testing.T) {
	obj := "\xef\xbb\xbf" + `{"Index":12,"PnPID":"PCI\\VEN","Profile":"Nhà Minh","WPDName":null}`
	m, err := parseEnrichment([]byte(obj))
	if err != nil || len(m) != 1 || m[12].Profile != "Nhà Minh" || m[12].WPDName != "" {
		t.Fatalf("object: %v %+v", err, m)
	}
	arr := `[{"Index":1,"Profile":"A"},{"Index":2,"WPDName":"vivo X300 Pro","ParentDesc":"x"},{"Index":3}]`
	m, err = parseEnrichment([]byte(arr))
	if err != nil || len(m) != 3 || m[2].WPDName != "vivo X300 Pro" || m[3].PnPID != "" {
		t.Fatalf("array: %v %+v", err, m)
	}
	for _, empty := range []string{"", "  \n", "null", "\xef\xbb\xbf"} {
		if m, err := parseEnrichment([]byte(empty)); err != nil || len(m) != 0 {
			t.Errorf("empty %q: %v %v", empty, err, m)
		}
	}
	if _, err := parseEnrichment([]byte("{bad")); err == nil {
		t.Error("want error on garbage")
	}
}
