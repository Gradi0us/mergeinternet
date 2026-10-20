// Package devinfo works out WHO is giving internet to the PC: which adapter is
// the "main" line (LAN / home Wi-Fi) and which are phones lending mobile data
// (USB tethering or Wi-Fi hotspot), with a human name for each.
package devinfo

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"time"
)

type Via string

const (
	ViaLAN     Via = "lan"
	ViaWiFi    Via = "wifi"
	ViaUSB     Via = "usb"
	ViaHotspot Via = "hotspot"
)

type Role string

const (
	RoleMain      Role = "main"
	RoleExtension Role = "extension"
	RoleIgnore    Role = "ignore"
)

type Device struct {
	Interface   string   `json:"interface"` // Windows alias, e.g. "Ethernet 3"
	Index       int      `json:"index"`
	Description string   `json:"description"` // adapter model
	Kind        string   `json:"kind"`        // bindif kind: wifi|ethernet|android|iphone|virtual
	Via         Via      `json:"via"`
	Role        Role     `json:"role"`   // suggested role
	Name        string   `json:"name"`   // human name to display
	Vendor      string   `json:"vendor"` // "Apple", "Samsung", ... or ""
	SSID        string   `json:"ssid"`   // Wi-Fi only
	Gateway     string   `json:"gateway"`
	IPv4        []string `json:"ipv4"` // excluding 169.254/16
	Up          bool     `json:"up"`
	Usable      bool     `json:"usable"` // up AND has a non-link-local IPv4
}

// rawAdapter is the plain input of classify (no OS calls needed to build it in tests).
type rawAdapter struct {
	Name        string
	Index       int
	Description string
	Kind        string
	Up          bool
	IPv4        []string
	Gateway     string
	PnPID       string // PnPDeviceID, e.g. USB\VID_05AC&PID_12A8&MI_00\...
	Profile     string // Get-NetConnectionProfile Name (SSID for Wi-Fi)
	WPDName     string // FriendlyName of the WPD device sharing the same USB parent
	ParentDesc  string // parent BusReportedDeviceDesc
	ParentName  string // parent FriendlyName
}

// extra is the output of the expensive enrichment, keyed by interface index.
type extra struct {
	Index      int    `json:"Index"`
	PnPID      string `json:"PnPID"`
	Profile    string `json:"Profile"`
	WPDName    string `json:"WPDName"`
	ParentID   string `json:"ParentID"`
	ParentDesc string `json:"ParentDesc"`
	ParentName string `json:"ParentName"`
}

type cheapFn func() ([]rawAdapter, error)
type enrichFn func(ctx context.Context) (map[int]extra, error)

func merge(raw []rawAdapter, ex map[int]extra) []rawAdapter {
	out := make([]rawAdapter, len(raw))
	copy(out, raw)
	for i := range out {
		if e, ok := ex[out[i].Index]; ok {
			out[i].PnPID = e.PnPID
			out[i].Profile = e.Profile
			out[i].WPDName = e.WPDName
			out[i].ParentDesc = e.ParentDesc
			out[i].ParentName = e.ParentName
		}
	}
	return out
}

// Detect runs the full detection (cheap pass + PowerShell enrichment on Windows).
func Detect(ctx context.Context) ([]Device, error) {
	return detectWith(ctx, cheapScan, enrichScan)
}

func detectWith(ctx context.Context, cheap cheapFn, enrich enrichFn) ([]Device, error) {
	raw, err := cheap()
	if err != nil {
		return nil, err
	}
	ex, _ := enrich(ctx) // enrichment failure is tolerated: names degrade
	return classify(merge(raw, ex)), nil
}

// fingerprint covers (interface index, IPv4, up) of every adapter.
func fingerprint(raw []rawAdapter) string {
	parts := make([]string, 0, len(raw))
	for _, r := range raw {
		s := strconv.Itoa(r.Index) + "|" + strconv.FormatBool(r.Up) + "|" + strings.Join(r.IPv4, ",")
		parts = append(parts, s)
	}
	sort.Strings(parts)
	return strings.Join(parts, ";")
}

const (
	enrichMaxAge  = 2 * time.Minute
	enrichBackoff = 10 * time.Second
	execTimeout   = 15 * time.Second
)
