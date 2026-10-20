package devinfo

import (
	"encoding/json"
	"net/netip"
	"sort"
	"strings"
	"unicode"
)

var hotspotKeywords = []string{"iphone", "ipad", "android", "androidap", "galaxy", "samsung", "vivo", "oppo",
	"realme", "redmi", "xiaomi", "poco", "mi ", "pixel", "huawei", "honor", "oneplus", "nokia", "motorola",
	"moto ", "sony", "xperia", "zte", "nubia", "tecno", "infinix", "itel"}

// USB vendor IDs (hwdata usb.ids) -> vendor.
var vidVendor = map[string]string{
	"05ac": "Apple", "04e8": "Samsung", "18d1": "Google", "2717": "Xiaomi", "22d9": "OPPO",
	"2a70": "OnePlus", "12d1": "Huawei", "22b8": "Motorola", "054c": "Sony", "19d2": "ZTE",
	"0b05": "ASUS", "0bb4": "HTC",
	"2d95": "vivo", // UNVERIFIED: commonly cited, not confirmed against usb.ids
}

// keyword -> vendor, checked in order against lowercase text.
var vendorKeywords = []struct{ kw, vendor string }{
	{"iphone", "Apple"}, {"ipad", "Apple"}, {"apple", "Apple"},
	{"samsung", "Samsung"}, {"galaxy", "Samsung"},
	{"vivo", "vivo"}, {"oppo", "OPPO"}, {"realme", "realme"},
	{"xiaomi", "Xiaomi"}, {"redmi", "Xiaomi"}, {"poco", "Xiaomi"},
	{"pixel", "Google"}, {"google", "Google"},
	{"huawei", "Huawei"}, {"honor", "Honor"}, {"oneplus", "OnePlus"},
	{"nokia", "Nokia"}, {"motorola", "Motorola"}, {"sony", "Sony"}, {"xperia", "Sony"},
}

func vendorFromText(texts ...string) string {
	for _, t := range texts {
		l := strings.ToLower(t)
		for _, k := range vendorKeywords {
			if strings.Contains(l, k.kw) {
				return k.vendor
			}
		}
	}
	return ""
}

func vendorFromPnP(pnp string) string {
	u := strings.ToUpper(pnp)
	i := strings.Index(u, "VID_")
	if !strings.HasPrefix(u, "USB\\") || i < 0 || len(u) < i+8 {
		return ""
	}
	return vidVendor[strings.ToLower(u[i+4:i+8])]
}

func isGeneric(s string) bool {
	l := strings.ToLower(strings.TrimSpace(s))
	if l == "" || l == "android" {
		return true
	}
	for _, g := range []string{"usb composite device", "network controller", "remote ndis", "mobile device ethernet",
		"composite", "generic", "usb input device"} {
		if strings.Contains(l, g) {
			return true
		}
	}
	for _, tok := range strings.FieldsFunc(l, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		if tok == "mtp" {
			return true
		}
	}
	return false
}

func containsFold(s string, subs ...string) bool {
	l := strings.ToLower(s)
	for _, x := range subs {
		if strings.Contains(l, strings.ToLower(x)) {
			return true
		}
	}
	return false
}

var iphoneNet = netip.MustParsePrefix("172.20.10.0/28")

func isPhoneHotspot(gateway, ssid string) bool {
	if a, err := netip.ParseAddr(gateway); err == nil && iphoneNet.Contains(a) {
		return true
	}
	l := strings.ToLower(ssid)
	for _, k := range hotspotKeywords {
		if strings.Contains(l, k) {
			return true
		}
	}
	return false
}

func usableIPs(ips []string) []string {
	var out []string
	for _, s := range ips {
		a, err := netip.ParseAddr(s)
		if err != nil || a.IsLinkLocalUnicast() || a.IsLoopback() {
			continue
		}
		out = append(out, s)
	}
	return out
}

func isUSBTether(r rawAdapter) bool {
	if r.Kind == "android" || r.Kind == "iphone" {
		return true
	}
	return strings.HasPrefix(strings.ToUpper(r.PnPID), "USB\\") &&
		containsFold(r.Description, "ndis", "mobile device ethernet")
}

func isVirtual(r rawAdapter) bool {
	u := strings.ToUpper(r.PnPID)
	return r.Kind == "virtual" || strings.HasPrefix(u, "ROOT\\") || strings.HasPrefix(u, "SWD\\")
}

func usbName(r rawAdapter, vendor string) string {
	for _, c := range []string{r.WPDName, r.ParentDesc, r.ParentName} {
		if !isGeneric(c) {
			return strings.TrimSpace(c)
		}
	}
	if vendor == "Apple" {
		return "iPhone"
	}
	if vendor != "" {
		return "Điện thoại " + vendor
	}
	if r.Description != "" {
		return r.Description
	}
	return r.Name
}

func nameOr(s, fallback string) string {
	if s = strings.TrimSpace(s); s != "" {
		return s
	}
	return fallback
}

// classify is a pure function: raw adapter data in, classified devices out.
func classify(in []rawAdapter) []Device {
	type item struct {
		ips    []string
		usable bool
		virt   bool
		usb    bool
	}
	items := make([]item, len(in))
	for i, r := range in {
		it := item{ips: usableIPs(r.IPv4)}
		it.usable = r.Up && len(it.ips) > 0
		it.virt = isVirtual(r)
		it.usb = !it.virt && isUSBTether(r)
		items[i] = it
	}

	devs := make([]Device, len(in))
	var cands []int // main candidates (non-hotspot Ethernet / Wi-Fi)
	for i, r := range in {
		it := items[i]
		d := Device{Interface: r.Name, Index: r.Index, Description: r.Description, Kind: r.Kind,
			Gateway: r.Gateway, IPv4: it.ips, Up: r.Up, Usable: it.usable}
		if d.IPv4 == nil {
			d.IPv4 = []string{}
		}
		switch {
		case it.virt:
			d.Role, d.Via = RoleIgnore, ViaLAN
			if r.Kind == "wifi" {
				d.Via = ViaWiFi
			}
			d.Name = nameOr(r.Description, r.Name)
		case it.usb:
			d.Role, d.Via = RoleExtension, ViaUSB
			d.Vendor = vendorFromPnP(r.PnPID)
			if d.Vendor == "" {
				d.Vendor = vendorFromText(r.WPDName, r.ParentDesc, r.ParentName, r.Description)
			}
			d.Name = usbName(r, d.Vendor)
		case r.Kind == "wifi":
			d.SSID = strings.TrimSpace(r.Profile)
			if isPhoneHotspot(r.Gateway, d.SSID) {
				d.Role, d.Via = RoleExtension, ViaHotspot
				d.Vendor = vendorFromText(d.SSID)
			} else {
				// A regular Wi-Fi network: the main line, or (when the PC also
				// has LAN) an extra network. Whether it is a different uplink
				// is checked by public IP when the lines are measured.
				d.Via = ViaWiFi
				cands = append(cands, i)
			}
			d.Name = nameOr(d.SSID, "Wi-Fi")
		default: // ethernet
			d.Via = ViaLAN
			d.Name = "Mạng LAN"
			cands = append(cands, i)
		}
		devs[i] = d
	}

	// exactly one main: usable first, Ethernet before Wi-Fi, then by name.
	key := func(i int) (int, int, string) {
		u, e := 1, 1
		if items[i].usable {
			u = 0
		}
		if devs[i].Via == ViaLAN {
			e = 0
		}
		return u, e, devs[i].Interface
	}
	sort.SliceStable(cands, func(a, b int) bool {
		ua, ea, na := key(cands[a])
		ub, eb, nb := key(cands[b])
		if ua != ub {
			return ua < ub
		}
		if ea != eb {
			return ea < eb
		}
		return na < nb
	})
	for n, i := range cands {
		if n == 0 {
			devs[i].Role = RoleMain
			continue
		}
		devs[i].Role = RoleExtension // an extra network next to the main line
		if devs[i].Via == ViaLAN {
			devs[i].Name = "Mạng LAN (" + devs[i].Interface + ")"
		}
	}
	sortDevices(devs)
	return devs
}

func roleOrder(r Role) int {
	switch r {
	case RoleMain:
		return 0
	case RoleExtension:
		return 1
	}
	return 2
}

func sortDevices(d []Device) {
	sort.SliceStable(d, func(i, j int) bool {
		if a, b := roleOrder(d[i].Role), roleOrder(d[j].Role); a != b {
			return a < b
		}
		return d[i].Interface < d[j].Interface
	})
}

// parseEnrichment parses the PowerShell JSON (bare object or array, optional BOM).
func parseEnrichment(b []byte) (map[int]extra, error) {
	b = []byte(strings.TrimSpace(strings.TrimPrefix(string(b), "\xef\xbb\xbf")))
	out := map[int]extra{}
	if len(b) == 0 || string(b) == "null" {
		return out, nil
	}
	var list []extra
	if b[0] == '[' {
		if err := json.Unmarshal(b, &list); err != nil {
			return nil, err
		}
	} else {
		var one extra
		if err := json.Unmarshal(b, &one); err != nil {
			return nil, err
		}
		list = []extra{one}
	}
	for _, e := range list {
		out[e.Index] = e
	}
	return out, nil
}
