// Package tunif wraps the WireGuard TUN driver (wintun on Windows, /dev/net/tun
// on Linux) with a simple one-packet-at-a-time API, and configures addresses
// and routes on it.
package tunif

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"

	"golang.zx2c4.com/wireguard/tun"
)

// Headroom in front of each packet; Linux needs >= 10 bytes for the virtio-net header.
const offset = 16

// Large enough for a GSO super-packet split by the Linux driver.
const bufSize = offset + 65535

type Device struct {
	dev  tun.Device
	name string

	// Read side (single reader goroutine).
	bufs  [][]byte
	sizes []int
	n     int
	pos   int

	writeMu sync.Mutex
}

func Open(name string, mtu int) (*Device, error) {
	dev, err := tun.CreateTUN(name, mtu)
	if err != nil {
		return nil, fmt.Errorf("create TUN %q: %w (Windows: run as Administrator and put wintun.dll next to the exe; Linux: run as root)", name, err)
	}
	real, err := dev.Name()
	if err != nil {
		real = name
	}
	batch := dev.BatchSize()
	d := &Device{dev: dev, name: real, bufs: make([][]byte, batch), sizes: make([]int, batch)}
	for i := range d.bufs {
		d.bufs[i] = make([]byte, bufSize)
	}
	return d, nil
}

func (d *Device) Name() string { return d.name }

// Read returns the next packet. The slice is only valid until the next Read.
func (d *Device) Read() ([]byte, error) {
	for d.pos >= d.n {
		n, err := d.dev.Read(d.bufs, d.sizes, offset)
		if err != nil {
			return nil, err
		}
		d.n, d.pos = n, 0
	}
	i := d.pos
	d.pos++
	return d.bufs[i][offset : offset+d.sizes[i]], nil
}

func (d *Device) Write(pkt []byte) error {
	buf := make([]byte, offset+len(pkt))
	copy(buf[offset:], pkt)
	d.writeMu.Lock()
	defer d.writeMu.Unlock()
	_, err := d.dev.Write([][]byte{buf}, offset)
	return err
}

func (d *Device) Close() error { return d.dev.Close() }

// run executes an OS configuration command with a timeout.
func run(name string, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}
