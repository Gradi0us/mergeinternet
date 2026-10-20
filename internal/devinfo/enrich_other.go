//go:build !windows

package devinfo

import (
	"context"
	"strings"

	"mergeinternet/internal/bindif"
)

// cheapScan on non-Windows: bindif data only (no gateway lookup).
func cheapScan() ([]rawAdapter, error) {
	list, err := bindif.List()
	if err != nil {
		return nil, err
	}
	out := make([]rawAdapter, 0, len(list))
	for _, i := range list {
		kind := string(i.Kind)
		if kind == string(bindif.KindEthernet) && (strings.HasPrefix(i.Name, "wlan") || strings.HasPrefix(i.Name, "wl")) {
			kind = string(bindif.KindWiFi)
		}
		r := rawAdapter{Name: i.Name, Index: i.Index, Description: i.Description, Kind: kind, Up: i.Up}
		for _, a := range i.IPv4 {
			r.IPv4 = append(r.IPv4, a.String())
		}
		out = append(out, r)
	}
	return out, nil
}

// enrichScan is a stub: no extra names off Windows.
func enrichScan(context.Context) (map[int]extra, error) { return map[int]extra{}, nil }
