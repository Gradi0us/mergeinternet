package bindif

import (
	"fmt"
	"net"

	"golang.org/x/sys/unix"
)

// pinToInterface uses SO_BINDTODEVICE (unprivileged since Linux 5.7).
func pinToInterface(fd uintptr, ifc *net.Interface) error {
	if err := unix.SetsockoptString(int(fd), unix.SOL_SOCKET, unix.SO_BINDTODEVICE, ifc.Name); err != nil {
		return fmt.Errorf("SO_BINDTODEVICE on %q: %w", ifc.Name, err)
	}
	return nil
}

func platformSocketSetup(uintptr) error { return nil }

func adapterDetails() map[int]adapterDetail { return nil }
