package localproxy

import (
	"errors"
	"io"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"time"
)

func (p *Proxy) socksLoop() {
	defer p.wg.Done()
	for {
		c, err := p.socksLn.Accept()
		if err != nil {
			select {
			case <-p.stopCh:
			default:
				p.fail(err)
			}
			return
		}
		if !p.begin() {
			c.Close()
			return
		}
		go func() {
			defer p.wg.Done()
			p.handleSocks(c)
		}()
	}
}

func socksReply(c net.Conn, code byte) {
	c.Write([]byte{5, code, 0, 1, 0, 0, 0, 0, 0, 0})
}

func (p *Proxy) handleSocks(c net.Conn) {
	untrack, ok := p.track(c)
	if !ok {
		return
	}
	defer untrack()
	defer c.Close()

	c.SetDeadline(time.Now().Add(handshakeLimit))
	var hdr [2]byte
	if _, err := io.ReadFull(c, hdr[:]); err != nil || hdr[0] != 5 {
		return
	}
	methods := make([]byte, hdr[1])
	if _, err := io.ReadFull(c, methods); err != nil {
		return
	}
	noAuth := false
	for _, m := range methods {
		if m == 0 {
			noAuth = true
		}
	}
	if !noAuth {
		c.Write([]byte{5, 0xFF})
		return
	}
	if _, err := c.Write([]byte{5, 0}); err != nil {
		return
	}
	var req [4]byte
	if _, err := io.ReadFull(c, req[:]); err != nil || req[0] != 5 {
		return
	}
	var host string
	switch req[3] {
	case 1:
		var b [4]byte
		if _, err := io.ReadFull(c, b[:]); err != nil {
			return
		}
		host = netip.AddrFrom4(b).String()
	case 4:
		var b [16]byte
		if _, err := io.ReadFull(c, b[:]); err != nil {
			return
		}
		host = netip.AddrFrom16(b).String()
	case 3:
		var l [1]byte
		if _, err := io.ReadFull(c, l[:]); err != nil || l[0] == 0 {
			return
		}
		b := make([]byte, l[0])
		if _, err := io.ReadFull(c, b); err != nil {
			return
		}
		host = string(b)
	default:
		socksReply(c, 8) // address type not supported
		return
	}
	var pb [2]byte
	if _, err := io.ReadFull(c, pb[:]); err != nil {
		return
	}
	port := int(pb[0])<<8 | int(pb[1])
	if req[1] != 1 {
		socksReply(c, 7) // command not supported
		return
	}
	target := net.JoinHostPort(host, strconv.Itoa(port))
	if port == 0 || refuseDest(target) {
		socksReply(c, 2) // not allowed by ruleset
		return
	}
	// The client may close early; cancel the dial then.
	up, err := p.dialBalanced(p.baseCtx, "tcp", target)
	if err != nil {
		p.logf("SOCKS %s: %v", target, err)
		if strings.Contains(err.Error(), "refused") {
			socksReply(c, 5)
		} else if errors.Is(err, errNoPath) {
			socksReply(c, 1)
		} else {
			socksReply(c, 4)
		}
		return
	}
	c.SetDeadline(time.Time{})
	if _, err := c.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0}); err != nil {
		up.Close()
		return
	}
	tunnel(c, up, nil)
}
