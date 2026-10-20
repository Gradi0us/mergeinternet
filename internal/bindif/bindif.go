// Package bindif opens UDP sockets whose traffic is forced out of one specific
// network interface, regardless of the routing table (which on the client
// points everything into the tunnel).
package bindif

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"syscall"
)

type adapterDetail struct {
	description string
	wireless    bool
}

type Info struct {
	Name        string
	Index       int
	Up          bool
	IPv4        []netip.Addr
	Description string // adapter model, e.g. "Apple Mobile Device Ethernet" (Windows only)
	Kind        Kind
}

// Kind is a best guess of what an interface is, so the UI can suggest paths.
type Kind string

const (
	KindWiFi     Kind = "wifi"
	KindEthernet Kind = "ethernet"
	KindAndroid  Kind = "android"
	KindIPhone   Kind = "iphone"
	KindVirtual  Kind = "virtual" // loopback, VPN, Hyper-V, Wi-Fi Direct... never a useful path
)

func classify(name, desc string, wireless bool) Kind {
	s := strings.ToLower(name + " " + desc)
	has := func(subs ...string) bool {
		for _, sub := range subs {
			if strings.Contains(s, sub) {
				return true
			}
		}
		return false
	}
	switch {
	case has("loopback", "wintun", "wireguard", "hyper-v", "vethernet", "virtual", "vmware",
		"virtualbox", "tap-", "bluetooth", "teredo", "isatap", "wi-fi direct", "docker", "mi0"):
		return KindVirtual
	case has("remote ndis", "rndis", "android"):
		return KindAndroid
	case has("apple mobile device", "iphone"):
		return KindIPhone
	case wireless || has("wi-fi", "wifi", "wireless", "wlan", "802.11"):
		return KindWiFi
	}
	return KindEthernet
}

// List returns all interfaces with their IPv4 addresses, for `mi-client interfaces`.
func List() ([]Info, error) {
	ifs, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	details := adapterDetails()
	var out []Info
	for _, ifc := range ifs {
		info := Info{Name: ifc.Name, Index: ifc.Index, Up: ifc.Flags&net.FlagUp != 0}
		d := details[ifc.Index]
		info.Description = d.description
		info.Kind = classify(ifc.Name, d.description, d.wireless)
		if ifc.Flags&net.FlagLoopback != 0 {
			info.Kind = KindVirtual
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok {
				if ip, ok := netip.AddrFromSlice(ipn.IP); ok && ip.Unmap().Is4() {
					info.IPv4 = append(info.IPv4, ip.Unmap())
				}
			}
		}
		out = append(out, info)
	}
	return out, nil
}

// interfaceIPv4 returns the interface and its first usable IPv4 address.
func interfaceIPv4(name string) (*net.Interface, netip.Addr, error) {
	ifc, err := net.InterfaceByName(name)
	if err != nil {
		return nil, netip.Addr{}, fmt.Errorf("interface %q: %w (see `mi-client interfaces`)", name, err)
	}
	addrs, err := ifc.Addrs()
	if err != nil {
		return nil, netip.Addr{}, err
	}
	for _, a := range addrs {
		ipn, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		ip, ok := netip.AddrFromSlice(ipn.IP)
		if !ok {
			continue
		}
		ip = ip.Unmap()
		if ip.Is4() && !ip.IsLinkLocalUnicast() {
			return ifc, ip, nil
		}
	}
	return nil, netip.Addr{}, fmt.Errorf("interface %q has no usable IPv4 address (tether not connected / no DHCP lease?)", name)
}

// DialerFor returns a net.Dialer whose TCP sockets are pinned to the named
// interface (IP_UNICAST_IF / SO_BINDTODEVICE) and bound to its IPv4 address.
// Use it with "tcp4". The UDP-only socket setup (SIO_UDP_CONNRESET) is not applied.
func DialerFor(name string) (*net.Dialer, netip.Addr, error) {
	ifc, ip, err := interfaceIPv4(name)
	if err != nil {
		return nil, netip.Addr{}, err
	}
	d := &net.Dialer{
		LocalAddr: &net.TCPAddr{IP: ip.AsSlice()},
		Control: func(_, _ string, c syscall.RawConn) error {
			var serr error
			if err := c.Control(func(fd uintptr) { serr = pinToInterface(fd, ifc) }); err != nil {
				return err
			}
			return serr
		},
	}
	return d, ip, nil
}

// ListenUDP opens an IPv4 UDP socket pinned to the named interface. An empty
// name opens an ordinary socket that follows the routing table.
// It returns the local address the socket is bound to.
func ListenUDP(ctx context.Context, name string) (*net.UDPConn, netip.Addr, error) {
	var (
		local   = netip.IPv4Unspecified()
		control func(fd uintptr) error
	)
	if name != "" {
		ifc, ip, err := interfaceIPv4(name)
		if err != nil {
			return nil, netip.Addr{}, err
		}
		local = ip
		control = func(fd uintptr) error { return pinToInterface(fd, ifc) }
	}
	lc := net.ListenConfig{Control: func(_, _ string, c syscall.RawConn) error {
		var serr error
		err := c.Control(func(fd uintptr) {
			if serr = platformSocketSetup(fd); serr == nil && control != nil {
				serr = control(fd)
			}
		})
		if err != nil {
			return err
		}
		return serr
	}}
	pc, err := lc.ListenPacket(ctx, "udp4", netip.AddrPortFrom(local, 0).String())
	if err != nil {
		return nil, netip.Addr{}, fmt.Errorf("listen on %q (%s): %w", name, local, err)
	}
	return pc.(*net.UDPConn), local, nil
}
