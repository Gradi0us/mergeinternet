//go:build !windows && !linux

package bindif

import "net"

// Elsewhere we only bind the interface's source address and rely on the OS
// routing it out of the matching interface.
func pinToInterface(uintptr, *net.Interface) error { return nil }

func platformSocketSetup(uintptr) error { return nil }

func adapterDetails() map[int]adapterDetail { return nil }
