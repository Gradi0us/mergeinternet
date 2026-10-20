package tunif

import (
	"net/netip"
	"strconv"
)

var defaultHalves = []string{"0.0.0.0/1", "128.0.0.0/1"}

func ConfigureServer(name string, addr netip.Prefix, mtu int) error {
	if err := run("ip", "addr", "replace", addr.String(), "dev", name); err != nil {
		return err
	}
	return run("ip", "link", "set", "dev", name, "mtu", strconv.Itoa(mtu), "up")
}

// ConfigureClient is provided for testing the client on Linux. DNS is left to the system.
func ConfigureClient(name string, addr netip.Prefix, gw netip.Addr, mtu int, _ []netip.Addr) (func(), error) {
	if err := ConfigureServer(name, addr, mtu); err != nil {
		return nil, err
	}
	for _, r := range defaultHalves {
		if err := run("ip", "route", "replace", r, "via", gw.String(), "dev", name); err != nil {
			return nil, err
		}
	}
	return func() {
		for _, r := range defaultHalves {
			_ = run("ip", "route", "del", r, "dev", name)
		}
	}, nil
}
