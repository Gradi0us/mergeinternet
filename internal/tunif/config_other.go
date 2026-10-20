//go:build !windows && !linux

package tunif

import (
	"errors"
	"net/netip"
)

var errUnsupported = errors.New("tunnel configuration is only implemented for Windows (client) and Linux")

func ConfigureServer(string, netip.Prefix, int) error { return errUnsupported }

func ConfigureClient(string, netip.Prefix, netip.Addr, int, []netip.Addr) (func(), error) {
	return nil, errUnsupported
}
