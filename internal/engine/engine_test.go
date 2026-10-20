package engine

import (
	"bytes"
	"context"
	"encoding/binary"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"mergeinternet/internal/sched"
)

// collector stands in for a TUN device.
type collector struct {
	mu  sync.Mutex
	got []uint32
}

func (c *collector) deliver(pkt []byte) error {
	c.mu.Lock()
	c.got = append(c.got, binary.BigEndian.Uint32(pkt))
	c.mu.Unlock()
	return nil
}

func (c *collector) snapshot() []uint32 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]uint32(nil), c.got...)
}

func (c *collector) reset() {
	c.mu.Lock()
	c.got = nil
	c.mu.Unlock()
}

func packet(i uint32) []byte {
	b := make([]byte, 64+int(i%1200))
	binary.BigEndian.PutUint32(b, i)
	return b
}

type harness struct {
	server, client     *Engine
	toServer, toClient collector
	clientConns        []*net.UDPConn
	cut                []atomic.Bool             // simulate a dead path (drop both directions)
	shape, shapeDown   [8]atomic.Pointer[shaper] // optional per-path rate limiters: client->server / server->client

	ctx   context.Context
	saddr netip.AddrPort

	srv       atomic.Pointer[Engine] // what the server socket feeds; swapped on restart
	newServer func() *Engine
}

// restartServer replaces the server with a fresh instance on the same socket.
func (h *harness) restartServer() {
	h.server = h.newServer()
	h.srv.Store(h.server)
}

func newHarness(t *testing.T, paths int, mode sched.Mode) *harness {
	t.Helper()
	key := bytes.Repeat([]byte{1}, 32)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	h := &harness{cut: make([]atomic.Bool, paths+4), ctx: ctx} // spare slots for addPath

	sconn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sconn.Close() })
	SetSocketBuffers(sconn)
	saddr := sconn.LocalAddr().(*net.UDPAddr).AddrPort()
	h.saddr = saddr

	h.newServer = func() *Engine {
		s, err := NewServer(Config{Key: key, Mode: mode, ReorderDelay: 50 * time.Millisecond, Deliver: h.toServer.deliver},
			func(b []byte, to netip.AddrPort) error { _, err := sconn.WriteToUDPAddrPort(b, to); return err })
		if err != nil {
			t.Fatal(err)
		}
		go s.Run(ctx)
		return s
	}
	h.restartServer()
	h.client, err = NewClient(Config{Key: key, Mode: mode, ReorderDelay: 50 * time.Millisecond, Deliver: h.toClient.deliver})
	if err != nil {
		t.Fatal(err)
	}

	go func() {
		buf := make([]byte, 65535)
		for {
			n, from, err := sconn.ReadFromUDPAddrPort(buf)
			if err != nil {
				return
			}
			h.srv.Load().Inbound(buf[:n], from)
		}
	}()

	for i := 0; i < paths; i++ {
		h.addPath(t, i)
	}
	go h.client.Run(ctx)
	h.waitAlive(t, paths)
	return h
}

// addPath opens a client socket, reads from it and registers it as link i.
// It is safe to call while the client is running (hot-add).
func (h *harness) addPath(t *testing.T, i int) {
	t.Helper()
	c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	SetSocketBuffers(c)
	h.clientConns = append(h.clientConns, c)
	cut := &h.cut[i]
	shape, shapeDown := &h.shape[i], &h.shapeDown[i]
	h.client.AddLink(uint8(i), "p", 10_000, 10_000, func(b []byte) error {
		if cut.Load() {
			return nil
		}
		if s := shape.Load(); s != nil && !s.allow(len(b)) {
			return nil
		}
		_, err := c.WriteToUDPAddrPort(b, h.saddr)
		return err
	})
	go func() {
		buf := make([]byte, 65535)
		for {
			n, from, err := c.ReadFromUDPAddrPort(buf)
			if err != nil {
				return
			}
			if s := shapeDown.Load(); s != nil && !s.allow(n) {
				continue
			}
			if !cut.Load() {
				h.client.Inbound(buf[:n], from)
			}
		}
	}()
}

func (h *harness) waitAlive(t *testing.T, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		links, _, _ := h.client.Stats()
		alive := 0
		for _, l := range links {
			if l.Alive {
				alive++
			}
		}
		if alive == want {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("client links not alive after 5s")
}

func waitCount(t *testing.T, c *collector, n int, rx *Engine) []uint32 {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if got := c.snapshot(); len(got) >= n {
			return got
		}
		time.Sleep(20 * time.Millisecond)
	}
	got := c.snapshot()
	links, rs, bad := rx.Stats()
	t.Fatalf("received %d of %d packets; receiver reorder=%+v bad=%d replay=%d links=%+v",
		len(got), n, rs, bad, rx.rxReplay.Load(), links)
	return nil
}

func assertInOrder(t *testing.T, got []uint32, from uint32) {
	t.Helper()
	for i, v := range got {
		if v != from+uint32(i) {
			t.Fatalf("packet %d = %d, want %d (out of order or lost)", i, v, from+uint32(i))
		}
	}
}

// sendPaced spreads packets over time like real traffic, so loopback socket
// buffers are not overrun (that would be a test artefact, not a tunnel bug).
func sendPaced(e *Engine, from, n uint32) {
	for i := from; i < from+n; i++ {
		e.SendPacket(packet(i))
		if i%100 == 99 {
			time.Sleep(2 * time.Millisecond)
		}
	}
}

// sendBursts offers n packets in bursts of 100 every 20 ms (several MB/s), fast
// enough that a capped link's weighted share exceeds its cap, so the token
// bucket - not the scheduler weights - is what limits it.
func sendBursts(e *Engine, from, n uint32) {
	for i := from; i < from+n; i++ {
		e.SendPacket(packet(i))
		if (i-from)%100 == 99 {
			time.Sleep(20 * time.Millisecond)
		}
	}
}

func TestBothDirectionsInOrderAcrossThreePaths(t *testing.T) {
	h := newHarness(t, 3, sched.Aggregate)
	const n = 3000

	sendPaced(h.client, 0, n)
	assertInOrder(t, waitCount(t, &h.toServer, n, h.server), 0)

	sendPaced(h.server, 0, n)
	assertInOrder(t, waitCount(t, &h.toClient, n, h.client), 0)

	// Every path carried traffic in both directions.
	clientLinks, _, _ := h.client.Stats()
	serverLinks, _, _ := h.server.Stats()
	if len(serverLinks) != 3 {
		t.Fatalf("server learned %d links, want 3", len(serverLinks))
	}
	for i := range clientLinks {
		if clientLinks[i].TxBytes < 100_000 || serverLinks[i].TxBytes < 100_000 {
			t.Errorf("path %d underused: client tx=%d server tx=%d", i, clientLinks[i].TxBytes, serverLinks[i].TxBytes)
		}
	}
}

func TestFailoverWhenAPathDies(t *testing.T) {
	h := newHarness(t, 3, sched.Aggregate)
	h.cut[1].Store(true)
	time.Sleep(DeadAfter + 2*healthTick) // both sides notice

	for _, e := range []*Engine{h.client, h.server} {
		links, _, _ := e.Stats()
		if links[1].Alive {
			t.Fatal("cut path still marked alive")
		}
	}
	sendPaced(h.client, 0, 1000)
	assertInOrder(t, waitCount(t, &h.toServer, 1000, h.server), 0)
	sendPaced(h.server, 0, 1000)
	assertInOrder(t, waitCount(t, &h.toClient, 1000, h.client), 0)

	// Path comes back and is used again.
	h.cut[1].Store(false)
	h.waitAlive(t, 3)
	time.Sleep(2 * healthTick)
	before, _, _ := h.client.Stats()
	h.toServer.reset()
	sendPaced(h.client, 1000, 1000)
	after, _, _ := h.client.Stats()
	t.Logf("client links after revival: %+v", after)
	assertInOrder(t, waitCount(t, &h.toServer, 1000, h.server), 1000)
	if after[1].TxBytes-before[1].TxBytes < 100_000 {
		t.Fatal("recovered path not used")
	}
}

func TestRedundantModeDeliversEachPacketOnce(t *testing.T) {
	h := newHarness(t, 3, sched.Redundant)
	sendPaced(h.client, 0, 1000)
	waitCount(t, &h.toServer, 1000, h.server)
	time.Sleep(100 * time.Millisecond) // let any extra copies arrive
	got := h.toServer.snapshot()
	if len(got) != 1000 {
		t.Fatalf("delivered %d packets, want exactly 1000", len(got))
	}
	assertInOrder(t, got, 0)
	if _, rs, _ := h.server.Stats(); rs.Duplicates+rs.Late < 1500 {
		t.Fatalf("expected ~2000 dropped copies, stats %+v", rs)
	}
}

func TestServerIgnoresOlderSessionAndAcceptsRestart(t *testing.T) {
	h := newHarness(t, 1, sched.Aggregate)
	old := h.client.session.Load()

	// A restarted client (newer session) takes over.
	h.client.session.Store(old + 1)
	h.client.seq.Store(0)
	h.client.counter.Store(0)
	h.client.reorder.Reset()
	h.client.sendProbes()
	h.waitAlive(t, 1)
	sendPaced(h.client, 0, 100)
	assertInOrder(t, waitCount(t, &h.toServer, 100, h.server), 0)

	// Packets from the previous instance are now rejected.
	h.client.session.Store(old)
	_, _, badBefore := h.server.Stats()
	h.client.SendPacket(packet(999))
	time.Sleep(100 * time.Millisecond)
	if _, _, bad := h.server.Stats(); bad == badBefore {
		t.Fatal("old session packet was accepted")
	}
}

func TestClientResyncsAfterServerRestart(t *testing.T) {
	h := newHarness(t, 3, sched.Aggregate)
	sendPaced(h.server, 0, 500) // downlink sequence is now well past 1
	waitCount(t, &h.toClient, 500, h.client)
	oldSession := h.client.session.Load()

	h.restartServer()
	deadline := time.Now().Add(5 * time.Second)
	for h.client.session.Load() == oldSession || h.server.session.Load() != h.client.session.Load() {
		if time.Now().After(deadline) {
			t.Fatal("client did not start a new session after the server restarted")
		}
		time.Sleep(20 * time.Millisecond)
	}
	h.waitAlive(t, 3)

	h.toServer.reset()
	h.toClient.reset()
	sendPaced(h.client, 0, 1000)
	assertInOrder(t, waitCount(t, &h.toServer, 1000, h.server), 0)
	sendPaced(h.server, 0, 1000)
	assertInOrder(t, waitCount(t, &h.toClient, 1000, h.client), 0)
}

func TestServerCounterNeverRestarts(t *testing.T) {
	a, _ := NewServer(Config{Key: make([]byte, 32), Deliver: func([]byte) error { return nil }}, nil)
	time.Sleep(time.Millisecond)
	b, _ := NewServer(Config{Key: make([]byte, 32), Deliver: func([]byte) error { return nil }}, nil)
	if b.counter.Load() <= a.counter.Load()+1_000_000 {
		t.Fatalf("restarted server counter %d does not clear previous %d", b.counter.Load(), a.counter.Load())
	}
}

func TestDisabledLinkCarriesNoTrafficEitherWay(t *testing.T) {
	h := newHarness(t, 3, sched.Aggregate)
	h.client.SetLinkEnabled(1, false)
	time.Sleep(DeadAfter + 2*healthTick) // server stops hearing probes on link 1

	if links, _, _ := h.server.Stats(); links[1].Alive {
		t.Fatal("server still considers the disabled link alive")
	}
	cBefore, _, _ := h.client.Stats()
	sBefore, _, _ := h.server.Stats()
	sendPaced(h.client, 0, 1000)
	assertInOrder(t, waitCount(t, &h.toServer, 1000, h.server), 0)
	sendPaced(h.server, 0, 1000)
	assertInOrder(t, waitCount(t, &h.toClient, 1000, h.client), 0)
	cAfter, _, _ := h.client.Stats()
	sAfter, _, _ := h.server.Stats()
	if cAfter[1].TxBytes != cBefore[1].TxBytes || sAfter[1].TxBytes != sBefore[1].TxBytes {
		t.Fatalf("disabled link still used: client tx %d->%d, server tx %d->%d",
			cBefore[1].TxBytes, cAfter[1].TxBytes, sBefore[1].TxBytes, sAfter[1].TxBytes)
	}
	if cAfter[1].Enabled {
		t.Fatal("stats report the link as enabled")
	}

	h.client.SetLinkEnabled(1, true)
	h.waitAlive(t, 3)
	time.Sleep(2 * healthTick)
	h.toServer.reset()
	sendPaced(h.client, 1000, 1000)
	assertInOrder(t, waitCount(t, &h.toServer, 1000, h.server), 1000)
	if again, _, _ := h.client.Stats(); again[1].TxBytes-cAfter[1].TxBytes < 100_000 {
		t.Fatal("re-enabled link not used")
	}
}

// waitServerLimit blocks until the server has learned link id's cap from a probe.
func (h *harness) waitServerLimit(t *testing.T, id int, want uint32) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if links, _, _ := h.server.Stats(); len(links) > id && links[id].LimitKbps == want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("server never learned limit %d kbps for link %d", want, id)
}

func TestLinkLimitCapsBothDirections(t *testing.T) {
	h := newHarness(t, 2, sched.Aggregate)
	const (
		limit = 2000 // kbit/s
		n     = 3000
		burst = 64 << 10 // allowed slack: bucket (32 KiB) + probes + rounding
	)
	if !h.client.SetLinkLimit(1, limit) {
		t.Fatal("SetLinkLimit: link not found")
	}
	if h.client.SetLinkLimit(9, limit) {
		t.Fatal("SetLinkLimit accepted an unknown link")
	}
	time.Sleep(2 * probeInterval) // the cap reaches the server in a probe
	h.waitServerLimit(t, 1, limit)

	budget := func(elapsed time.Duration) uint64 {
		return uint64(float64(limit)*1000/8*elapsed.Seconds()) + burst
	}

	// Upload.
	before, _, _ := h.client.Stats()
	start := time.Now()
	sendBursts(h.client, 0, n)
	assertInOrder(t, waitCount(t, &h.toServer, n, h.server), 0)
	elapsed := time.Since(start)
	after, _, _ := h.client.Stats()
	up, upOther := after[1].TxBytes-before[1].TxBytes, after[0].TxBytes-before[0].TxBytes
	t.Logf("upload: limited link %d B in %v (budget %d), other link %d B", up, elapsed, budget(elapsed), upOther)
	if up > budget(elapsed) {
		t.Errorf("upload on limited link = %d B, budget %d B", up, budget(elapsed))
	}
	if floor := budget(elapsed) / 2; up < floor {
		t.Errorf("upload on limited link = %d B, expected the cap to be the binding limit (>= %d B)", up, floor)
	}
	if upOther < 3*up {
		t.Errorf("limited link should still carry something, with the unlimited one absorbing the rest: %d vs %d", up, upOther)
	}
	if links, _, _ := h.client.Stats(); links[1].LimitKbps != limit {
		t.Errorf("client stats LimitKbps = %d", links[1].LimitKbps)
	}

	// Download.
	sBefore, _, _ := h.server.Stats()
	start = time.Now()
	sendBursts(h.server, 0, n)
	assertInOrder(t, waitCount(t, &h.toClient, n, h.client), 0)
	elapsed = time.Since(start)
	sAfter, _, _ := h.server.Stats()
	down, downOther := sAfter[1].TxBytes-sBefore[1].TxBytes, sAfter[0].TxBytes-sBefore[0].TxBytes
	t.Logf("download: limited link %d B in %v (budget %d), other link %d B", down, elapsed, budget(elapsed), downOther)
	if down > budget(elapsed) {
		t.Errorf("download on limited link = %d B, budget %d B", down, budget(elapsed))
	}
	if floor := budget(elapsed) / 2; down < floor {
		t.Errorf("download on limited link = %d B, expected the cap to be the binding limit (>= %d B)", down, floor)
	}
	if downOther < 3*down {
		t.Errorf("download split wrong: %d vs %d", down, downOther)
	}
	if d := h.client.Dropped() + h.server.Dropped(); d != 0 {
		t.Errorf("%d packets dropped although an unlimited link was available", d)
	}

	// Removing the cap is advertised too.
	h.client.SetLinkLimit(1, 0)
	h.waitServerLimit(t, 1, 0)
}

// When every link is capped the sender drops excess packets. Those drops must
// not leave sequence gaps, or the receiver stalls all later packets for
// ReorderDelay waiting for packets that were never sent.
func TestPolicedDropsLeaveNoSequenceGaps(t *testing.T) {
	h := newHarness(t, 2, sched.Aggregate)
	h.client.SetLinkLimit(0, 1000)
	h.client.SetLinkLimit(1, 1000)
	const n = 3000
	sendBursts(h.client, 0, n)
	dropped := h.client.Dropped()
	if dropped == 0 {
		t.Fatal("expected the caps to force drops")
	}
	want := n - int(dropped)
	got := waitCount(t, &h.toServer, want, h.server)
	time.Sleep(100 * time.Millisecond)
	if _, rs, _ := h.server.Stats(); rs.Skipped != 0 {
		t.Fatalf("receiver skipped %d sequence numbers (sender dropped %d): drops left gaps", rs.Skipped, dropped)
	}
	for i := 1; i < len(got); i++ {
		if got[i] <= got[i-1] {
			t.Fatalf("out of order at %d: %d after %d", i, got[i], got[i-1])
		}
	}
}

func TestHotAddLinkWhileRunning(t *testing.T) {
	h := newHarness(t, 2, sched.Aggregate)

	// Traffic keeps flowing while the third link is plugged in.
	done := make(chan struct{})
	go func() {
		defer close(done)
		sendPaced(h.client, 0, 1500)
	}()
	h.addPath(t, 2)
	h.waitAlive(t, 3)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("sender stuck")
	}
	assertInOrder(t, waitCount(t, &h.toServer, 1500, h.server), 0)

	before, _, _ := h.client.Stats()
	sBefore, _, _ := h.server.Stats()
	if len(before) != 3 || len(sBefore) != 3 {
		t.Fatalf("client sees %d links, server %d, want 3", len(before), len(sBefore))
	}
	sendPaced(h.client, 1500, 3000)
	assertInOrder(t, waitCount(t, &h.toServer, 4500, h.server), 0)
	sendPaced(h.server, 0, 3000)
	assertInOrder(t, waitCount(t, &h.toClient, 3000, h.client), 0)

	after, _, _ := h.client.Stats()
	sAfter, _, _ := h.server.Stats()
	if up := after[2].TxBytes - before[2].TxBytes; up < 50_000 {
		t.Errorf("hot-added link carried only %d B upstream", up)
	}
	if down := sAfter[2].TxBytes - sBefore[2].TxBytes; down < 50_000 {
		t.Errorf("hot-added link carried only %d B downstream", down)
	}
}
