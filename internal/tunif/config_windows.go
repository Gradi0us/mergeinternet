package tunif

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"time"
)

// halves of 0.0.0.0/0: more specific than any adapter's default route, so all
// IPv4 traffic enters the tunnel without deleting the real default routes
// that the per-path sockets still need.
var defaultHalves = []string{"0.0.0.0/1", "128.0.0.0/1"}

// ConfigureClient assigns the tunnel address, makes the tunnel the default
// route and optionally sets its DNS servers. The returned cleanup removes the routes.
func ConfigureClient(name string, addr netip.Prefix, gw netip.Addr, mtu int, dns []netip.Addr) (func(), error) {
	mask := net.IP(net.CIDRMask(addr.Bits(), 32)).String()
	setAddr := []string{"interface", "ipv4", "set", "address", "name=" + name, "source=static",
		"address=" + addr.Addr().String(), "mask=" + mask, "gateway=none"}

	// The wintun adapter can take a moment to become visible to netsh.
	var err error
	for i := 0; i < 20; i++ {
		if err = run("netsh", setAddr...); err == nil {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if err != nil {
		return nil, err
	}
	steps := [][]string{
		{"interface", "ipv4", "set", "subinterface", name, fmt.Sprintf("mtu=%d", mtu), "store=active"},
		{"interface", "ipv4", "set", "interface", name, "metric=1"},
	}
	if len(dns) > 0 {
		steps = append(steps, []string{"interface", "ipv4", "set", "dnsservers", "name=" + name,
			"source=static", "address=" + dns[0].String(), "register=none", "validate=no"})
		for i, d := range dns[1:] {
			steps = append(steps, []string{"interface", "ipv4", "add", "dnsservers", "name=" + name,
				"address=" + d.String(), fmt.Sprintf("index=%d", i+2), "validate=no"})
		}
	}
	for _, r := range defaultHalves {
		steps = append(steps, []string{"interface", "ipv4", "add", "route", r, name, gw.String(), "metric=1", "store=active"})
	}
	for _, s := range steps {
		if err := run("netsh", s...); err != nil {
			return nil, err
		}
	}
	cleanup := func() {
		for _, r := range defaultHalves {
			_ = run("netsh", "interface", "ipv4", "delete", "route", r, name, gw.String())
		}
	}
	return cleanup, nil
}

func ConfigureServer(string, netip.Prefix, int) error {
	return errors.New("mi-server runs on Linux only")
}
