package bindif

import (
	"encoding/binary"
	"fmt"
	"net"
	"unsafe"

	"golang.org/x/sys/windows"
)

// ipUnicastIf forces the egress interface of an IPv4 socket (same technique as wireguard-windows).
const ipUnicastIf = 31

func pinToInterface(fd uintptr, ifc *net.Interface) error {
	// MSDN: for IPv4 the index must be in network byte order.
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], uint32(ifc.Index))
	idx := *(*uint32)(unsafe.Pointer(&b[0]))
	if err := windows.SetsockoptInt(windows.Handle(fd), windows.IPPROTO_IP, ipUnicastIf, int(idx)); err != nil {
		return fmt.Errorf("IP_UNICAST_IF on %q: %w", ifc.Name, err)
	}
	return nil
}

// adapterDetails returns the adapter description and type per interface index.
func adapterDetails() map[int]adapterDetail {
	out := map[int]adapterDetail{}
	size := uint32(16 << 10)
	for i := 0; i < 3; i++ {
		buf := make([]byte, size)
		first := (*windows.IpAdapterAddresses)(unsafe.Pointer(&buf[0]))
		err := windows.GetAdaptersAddresses(windows.AF_UNSPEC, 0, 0, first, &size)
		if err == windows.ERROR_BUFFER_OVERFLOW {
			continue
		}
		if err != nil {
			return out
		}
		for a := first; a != nil; a = a.Next {
			out[int(a.IfIndex)] = adapterDetail{
				description: windows.UTF16PtrToString(a.Description),
				wireless:    a.IfType == windows.IF_TYPE_IEEE80211,
			}
		}
		return out
	}
	return out
}

// platformSocketSetup stops Windows from failing later reads with
// WSAECONNRESET after an ICMP "port unreachable" (e.g. server restarting).
func platformSocketSetup(fd uintptr) error {
	var ret uint32
	flag := uint32(0)
	size := uint32(unsafe.Sizeof(flag))
	return windows.WSAIoctl(windows.Handle(fd), windows.SIO_UDP_CONNRESET,
		(*byte)(unsafe.Pointer(&flag)), size, nil, 0, &ret, nil, 0)
}
