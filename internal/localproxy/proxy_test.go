package localproxy

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"mergeinternet/internal/config"
)

type env struct {
	t      *testing.T
	p      *Proxy
	echo   net.Listener
	src    net.Listener // endless zero source
	web    *httptest.Server
	dials  [8]atomic.Int64
	fail   [8]atomic.Bool
	hosts  map[string]string
	gotHdr atomic.Value
}

func newEnv(t *testing.T, mut func(*Options), weights ...float64) *env {
	t.Helper()
	e := &env{t: t}
	var err error
	if e.echo, err = net.Listen("tcp", "127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			c, err := e.echo.Accept()
			if err != nil {
				return
			}
			go func() { defer c.Close(); io.Copy(c, c) }()
		}
	}()
	if e.src, err = net.Listen("tcp", "127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			c, err := e.src.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				buf := make([]byte, 64<<10)
				for {
					if _, err := c.Write(buf); err != nil {
						return
					}
				}
			}()
		}
	}()
	e.web = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e.gotHdr.Store(r.Header.Clone())
		w.Header().Set("Connection", "close")
		w.Header().Set("Proxy-Authenticate", "x")
		w.Header().Set("X-Reply", "ok")
		fmt.Fprintf(w, "path=%s q=%s", r.URL.Path, r.URL.RawQuery)
	}))
	e.hosts = map[string]string{
		"echo.test": e.echo.Addr().String(),
		"src.test":  e.src.Addr().String(),
		"web.test":  e.web.Listener.Addr().String(),
	}
	var paths []config.Path
	for i, w := range weights {
		paths = append(paths, config.Path{Name: fmt.Sprint("p", i), DownMbps: w})
	}
	opts := Options{
		HTTPAddr: "127.0.0.1:0", SOCKSAddr: "127.0.0.1:0", Paths: paths, noHealth: true,
		Logf: t.Logf,
		dial: func(ctx context.Context, id uint8, network, addr string) (net.Conn, error) {
			e.dials[id].Add(1)
			if e.fail[id].Load() {
				return nil, fmt.Errorf("fake: path %d down", id)
			}
			host, _, _ := net.SplitHostPort(addr)
			target, ok := e.hosts[host]
			if !ok {
				target = e.hosts["echo.test"] // any other name/IP -> echo
			}
			var d net.Dialer
			return d.DialContext(ctx, "tcp", target)
		},
	}
	if mut != nil {
		mut(&opts)
	}
	if e.p, err = Start(opts); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		e.p.Stop()
		e.echo.Close()
		e.src.Close()
		e.web.Close()
	})
	return e
}

// connect opens a CONNECT tunnel through the HTTP proxy.
func (e *env) connect(target string) (net.Conn, *bufio.Reader, string) {
	e.t.Helper()
	c, err := net.Dial("tcp", e.p.HTTPAddr())
	if err != nil {
		e.t.Fatal(err)
	}
	fmt.Fprintf(c, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target, target)
	br := bufio.NewReader(c)
	line, err := br.ReadString('\n')
	if err != nil {
		e.t.Fatalf("read status: %v", err)
	}
	for {
		l, err := br.ReadString('\n')
		if err != nil || l == "\r\n" {
			break
		}
	}
	return c, br, strings.TrimSpace(line)
}

func (e *env) openMany(n int, target string) []net.Conn {
	e.t.Helper()
	conns := make([]net.Conn, n)
	var wg sync.WaitGroup
	for i := range conns {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, br, st := e.connect(target)
			if !strings.Contains(st, "200") {
				e.t.Errorf("status %q", st)
				return
			}
			// round-trip so the tunnel is surely established
			c.Write([]byte("x"))
			if b, err := br.ReadByte(); err != nil || b != 'x' {
				e.t.Errorf("echo: %v", err)
			}
			conns[i] = c
		}()
	}
	wg.Wait()
	return conns
}

func closeAll(cs []net.Conn) {
	for _, c := range cs {
		if c != nil {
			c.Close()
		}
	}
}

func TestConnectCountsBytes(t *testing.T) {
	e := newEnv(t, nil, 10)
	c, br, st := e.connect("echo.test:443")
	defer c.Close()
	if !strings.Contains(st, "200") {
		t.Fatalf("status %q", st)
	}
	msg := strings.Repeat("hello", 1000)
	c.Write([]byte(msg))
	buf := make([]byte, len(msg))
	if _, err := io.ReadFull(br, buf); err != nil || string(buf) != msg {
		t.Fatalf("echo mismatch: %v", err)
	}
	s := e.p.Stats()[0]
	if s.TxBytes != uint64(len(msg)) || s.RxBytes != uint64(len(msg)) || s.ActiveConns != 1 {
		t.Fatalf("stats %+v, want tx=rx=%d active=1", s, len(msg))
	}
	c.Close()
	deadline := time.Now().Add(2 * time.Second)
	for e.p.Stats()[0].ActiveConns != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if a := e.p.Stats()[0].ActiveConns; a != 0 {
		t.Fatalf("active conns after close = %d", a)
	}
}

func TestSpreadByWeight(t *testing.T) {
	e := newEnv(t, nil, 60, 30, 10)
	cs := e.openMany(30, "echo.test:443")
	defer closeAll(cs)
	got := [3]int64{e.dials[0].Load(), e.dials[1].Load(), e.dials[2].Load()}
	t.Logf("dial counts by path (want ~18/9/3): %v", got)
	if got[0] < 15 || got[0] > 21 || got[1] < 6 || got[1] > 12 || got[2] < 1 || got[2] > 6 {
		t.Fatalf("not spread by weight: %v", got)
	}
}

func TestDisabledPathGetsNothing(t *testing.T) {
	e := newEnv(t, nil, 50, 50)
	if err := e.p.SetEnabled(0, false); err != nil {
		t.Fatal(err)
	}
	cs := e.openMany(10, "echo.test:443")
	defer closeAll(cs)
	if e.dials[0].Load() != 0 || e.dials[1].Load() != 10 {
		t.Fatalf("dials: p0=%d p1=%d", e.dials[0].Load(), e.dials[1].Load())
	}
	if e.p.Stats()[0].Enabled {
		t.Fatal("Stats says enabled")
	}
	if err := e.p.SetEnabled(9, true); err == nil {
		t.Fatal("unknown id accepted")
	}
}

func TestFailedDialRetriesOnOtherPath(t *testing.T) {
	e := newEnv(t, nil, 100, 1)
	e.fail[0].Store(true)
	cs := e.openMany(3, "echo.test:443")
	defer closeAll(cs)
	st := e.p.Stats()
	if st[0].Alive {
		t.Fatal("failing path still alive")
	}
	if !st[1].Alive || st[1].ActiveConns != 3 {
		t.Fatalf("path1 stats %+v", st[1])
	}
	if e.dials[0].Load() != 1 {
		t.Fatalf("dead path was dialed %d times, want 1 (then skipped)", e.dials[0].Load())
	}
	if st[0].ActiveConns != 0 {
		t.Fatalf("leaked slot on failed path: %+v", st[0])
	}
	// every path failing -> 502
	e.fail[1].Store(true)
	c, _, status := e.connect("echo.test:443")
	c.Close()
	if !strings.Contains(status, "502") {
		t.Fatalf("status %q, want 502", status)
	}
}

func TestHealthCheck(t *testing.T) {
	e := newEnv(t, nil, 10, 10)
	e.fail[1].Store(true)
	ps0, _ := e.p.get(0)
	ps1, _ := e.p.get(1)
	e.p.checkPath(ps0)
	e.p.checkPath(ps1)
	st := e.p.Stats()
	if !st[0].Alive || st[0].RTT < 0 || st[1].Alive {
		t.Fatalf("stats %+v", st)
	}
	e.fail[1].Store(false)
	e.p.checkPath(ps1)
	if !e.p.Stats()[1].Alive {
		t.Fatal("path did not recover")
	}
}

func TestAbsoluteURIForward(t *testing.T) {
	e := newEnv(t, nil, 10)
	c, err := net.Dial("tcp", e.p.HTTPAddr())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	fmt.Fprint(c, "GET http://web.test/hello?x=1 HTTP/1.1\r\nHost: web.test\r\nProxy-Connection: keep-alive\r\n"+
		"Connection: X-Foo\r\nX-Foo: bar\r\nX-Keep: yes\r\nProxy-Authorization: Basic eA==\r\n\r\n")
	resp, err := http.ReadResponse(bufio.NewReader(c), nil)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || string(body) != "path=/hello q=x=1" {
		t.Fatalf("resp %d %q", resp.StatusCode, body)
	}
	if resp.Header.Get("X-Reply") != "ok" || resp.Header.Get("Proxy-Authenticate") != "" {
		t.Fatalf("response headers not filtered: %v", resp.Header)
	}
	h := e.gotHdr.Load().(http.Header)
	for _, bad := range []string{"X-Foo", "Proxy-Connection", "Proxy-Authorization"} {
		if h.Get(bad) != "" {
			t.Errorf("upstream saw hop-by-hop header %s", bad)
		}
	}
	if h.Get("X-Keep") != "yes" {
		t.Errorf("end-to-end header lost: %v", h)
	}
	if rx := e.p.Stats()[0].RxBytes; rx == 0 {
		t.Error("no bytes counted for forwarded request")
	}

	// the stdlib client also works through it
	pu, _ := url.Parse("http://" + e.p.HTTPAddr())
	cl := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(pu)}}
	r2, err := cl.Get("http://web.test/again")
	if err != nil {
		t.Fatal(err)
	}
	b2, _ := io.ReadAll(r2.Body)
	r2.Body.Close()
	if string(b2) != "path=/again q=" {
		t.Fatalf("stdlib client body %q", b2)
	}

	// origin-form request = someone using the proxy as a web server
	c2, _ := net.Dial("tcp", e.p.HTTPAddr())
	defer c2.Close()
	fmt.Fprint(c2, "GET /x HTTP/1.1\r\nHost: web.test\r\n\r\n")
	r3, err := http.ReadResponse(bufio.NewReader(c2), nil)
	if err != nil || r3.StatusCode != 400 {
		t.Fatalf("origin-form: %v %v", r3, err)
	}
}

func TestLoopbackAndSelfRefused(t *testing.T) {
	e := newEnv(t, nil, 10)
	_, port, _ := net.SplitHostPort(e.p.HTTPAddr())
	for _, tgt := range []string{"127.0.0.1:80", "[::1]:80", "localhost:" + port, "127.0.0.1:" + port, "0.0.0.0:80"} {
		c, _, st := e.connect(tgt)
		c.Close()
		if !strings.Contains(st, "403") {
			t.Errorf("CONNECT %s -> %q, want 403", tgt, st)
		}
	}
	for _, u := range []string{"http://127.0.0.1:" + port + "/", "http://localhost/", "http://[::1]/"} {
		c, _ := net.Dial("tcp", e.p.HTTPAddr())
		fmt.Fprintf(c, "GET %s HTTP/1.1\r\nHost: x\r\n\r\n", u)
		r, err := http.ReadResponse(bufio.NewReader(c), nil)
		c.Close()
		if err != nil || r.StatusCode != 403 {
			t.Errorf("GET %s -> %v %v, want 403", u, r, err)
		}
	}
	// SOCKS too
	sc, br := socksHandshake(t, e)
	defer sc.Close()
	rep := socksRequest(t, sc, br, 1, 1, []byte{127, 0, 0, 1}, 80)
	if rep != 2 {
		t.Errorf("socks to loopback reply %d, want 2", rep)
	}
	if n := e.dials[0].Load(); n != 0 {
		t.Fatalf("refused destinations were dialed %d times", n)
	}
}

func socksHandshake(t *testing.T, e *env) (net.Conn, *bufio.Reader) {
	t.Helper()
	c, err := net.Dial("tcp", e.p.SOCKSAddr())
	if err != nil {
		t.Fatal(err)
	}
	c.Write([]byte{5, 1, 0})
	br := bufio.NewReader(c)
	var r [2]byte
	if _, err := io.ReadFull(br, r[:]); err != nil || r != [2]byte{5, 0} {
		t.Fatalf("method reply %v %v", r, err)
	}
	return c, br
}

// socksRequest sends a request (atyp 1 ip / 3 domain) and returns the reply code.
func socksRequest(t *testing.T, c net.Conn, br *bufio.Reader, cmd, atyp byte, addr []byte, port int) byte {
	t.Helper()
	req := []byte{5, cmd, 0, atyp}
	if atyp == 3 {
		req = append(req, byte(len(addr)))
	}
	req = append(req, addr...)
	req = append(req, byte(port>>8), byte(port))
	c.Write(req)
	var rep [10]byte
	if _, err := io.ReadFull(br, rep[:]); err != nil {
		t.Fatalf("socks reply: %v", err)
	}
	return rep[1]
}

func TestSOCKS5(t *testing.T) {
	e := newEnv(t, nil, 10)
	// domain
	c, br := socksHandshake(t, e)
	if rep := socksRequest(t, c, br, 1, 3, []byte("echo.test"), 443); rep != 0 {
		t.Fatalf("domain CONNECT reply %d", rep)
	}
	c.Write([]byte("ping"))
	b := make([]byte, 4)
	if _, err := io.ReadFull(br, b); err != nil || string(b) != "ping" {
		t.Fatalf("domain echo %q %v", b, err)
	}
	c.Close()
	// IPv4
	c, br = socksHandshake(t, e)
	if rep := socksRequest(t, c, br, 1, 1, []byte{10, 1, 2, 3}, 80); rep != 0 {
		t.Fatalf("ipv4 CONNECT reply %d", rep)
	}
	c.Write([]byte("pong"))
	if _, err := io.ReadFull(br, b); err != nil || string(b) != "pong" {
		t.Fatalf("ipv4 echo %q %v", b, err)
	}
	c.Close()
	// BIND and UDP ASSOCIATE -> command not supported
	for _, cmd := range []byte{2, 3} {
		c, br = socksHandshake(t, e)
		if rep := socksRequest(t, c, br, cmd, 1, []byte{10, 1, 2, 3}, 80); rep != 7 {
			t.Fatalf("cmd %d reply %d, want 7", cmd, rep)
		}
		c.Close()
	}
	// no acceptable auth method
	c, _ = net.Dial("tcp", e.p.SOCKSAddr())
	c.Write([]byte{5, 1, 2})
	var r [2]byte
	io.ReadFull(c, r[:])
	c.Close()
	if r != [2]byte{5, 0xFF} {
		t.Fatalf("auth reject reply %v", r)
	}
	if e.p.Stats()[0].RxBytes == 0 {
		t.Fatal("socks bytes not counted")
	}
	// all paths down -> failure reply, not a hang
	e.fail[0].Store(true)
	c, br = socksHandshake(t, e)
	if rep := socksRequest(t, c, br, 1, 3, []byte("echo.test"), 443); rep == 0 {
		t.Fatal("expected failure reply")
	}
	c.Close()
}

func TestSetLimitCapsThroughput(t *testing.T) {
	e := newEnv(t, nil, 50)
	if err := e.p.SetLimit(0, 8); err != nil {
		t.Fatal(err)
	}
	if got := e.p.Stats()[0].LimitMbps; got != 8 {
		t.Fatalf("LimitMbps %v", got)
	}
	c, br, st := e.connect("src.test:80")
	defer c.Close()
	if !strings.Contains(st, "200") {
		t.Fatal(st)
	}
	start := time.Now()
	c.SetReadDeadline(start.Add(3 * time.Second))
	n, _ := io.Copy(io.Discard, br)
	el := time.Since(start).Seconds()
	limit := 8e6 / 8 // bytes/s
	burst := limit/10 + 64<<10
	t.Logf("received %d bytes in %.2fs = %.2f Mbit/s (limit 8)", n, el, float64(n)*8/el/1e6)
	if float64(n) > limit*el+burst {
		t.Fatalf("limit exceeded: %d > %.0f", n, limit*el+burst)
	}
	if float64(n) < limit*el*0.6 {
		t.Fatalf("throughput far below limit: %d", n)
	}
	// unlimited again -> much faster
	e.p.SetLimit(0, 0)
	c2, br2, _ := e.connect("src.test:80")
	defer c2.Close()
	c2.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	n2, _ := io.Copy(io.Discard, br2)
	if n2 < 3<<20 {
		t.Fatalf("unlimited path only moved %d bytes in 0.5s", n2)
	}
}

func TestStickyKeepsHostOnOnePath(t *testing.T) {
	e := newEnv(t, func(o *Options) { o.Sticky = true }, 50, 50, 50)
	cs := e.openMany(9, "echo.test:443")
	defer closeAll(cs)
	nz := 0
	for i := 0; i < 3; i++ {
		if e.dials[i].Load() > 0 {
			nz++
		}
	}
	if nz != 1 {
		t.Fatalf("sticky host spread over %d paths: %d/%d/%d", nz, e.dials[0].Load(), e.dials[1].Load(), e.dials[2].Load())
	}
	c, _, _ := e.connect("web.test:443")
	defer c.Close()
	if e.dials[0].Load()+e.dials[1].Load()+e.dials[2].Load() != 10 {
		t.Fatal("unexpected dial count")
	}
}

func TestStopClosesTunnelsAndDone(t *testing.T) {
	e := newEnv(t, nil, 10, 10)
	cs := e.openMany(4, "echo.test:443")
	defer closeAll(cs)
	sc, br := socksHandshake(t, e)
	defer sc.Close()
	if rep := socksRequest(t, sc, br, 1, 3, []byte("src.test"), 80); rep != 0 {
		t.Fatalf("socks reply %d", rep)
	}
	start := time.Now()
	e.p.Stop()
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("Stop took %v", d)
	}
	select {
	case <-e.p.Done():
	default:
		t.Fatal("Done not closed")
	}
	e.p.Stop() // idempotent
	if e.p.Err() != nil {
		t.Fatalf("Err = %v", e.p.Err())
	}
	for _, c := range cs {
		c.SetReadDeadline(time.Now().Add(time.Second))
		if _, err := c.Read(make([]byte, 1)); err == nil {
			t.Fatal("tunnel still readable after Stop")
		} else if ne, ok := err.(net.Error); ok && ne.Timeout() {
			t.Fatal("tunnel not closed by Stop")
		}
	}
	if c, err := net.DialTimeout("tcp", e.p.HTTPAddr(), 300*time.Millisecond); err == nil {
		c.Close()
		t.Fatal("listener still open")
	}
	for _, s := range e.p.Stats() {
		if s.ActiveConns != 0 {
			t.Fatalf("active conns after Stop: %+v", s)
		}
	}
}

func TestStartRejectsBadAddrs(t *testing.T) {
	for _, o := range []Options{
		{HTTPAddr: "0.0.0.0:0"},
		{HTTPAddr: "192.168.1.10:0"},
		{HTTPAddr: ":0"},
		{HTTPAddr: "127.0.0.1:0", SOCKSAddr: "0.0.0.0:0"},
		{HTTPAddr: ""},
	} {
		o.noHealth = true
		if p, err := Start(o); err == nil {
			p.Stop()
			t.Errorf("Start(%+v) accepted", o)
		}
	}
	e := newEnv(t, nil, 10)
	if p, err := Start(Options{HTTPAddr: e.p.HTTPAddr(), noHealth: true}); err == nil {
		p.Stop()
		t.Fatal("busy port accepted")
	}
}

func TestAddPathAndWeight(t *testing.T) {
	e := newEnv(t, nil, 10)
	id, err := e.p.AddPath(config.Path{Name: "new", DownMbps: 1000})
	if err != nil || id != 1 {
		t.Fatalf("AddPath = %d, %v", id, err)
	}
	cs := e.openMany(10, "echo.test:443")
	defer closeAll(cs)
	if e.dials[1].Load() < 8 {
		t.Fatalf("heavy new path got only %d/10", e.dials[1].Load())
	}
	if err := e.p.SetWeight(1, 1); err != nil {
		t.Fatal(err)
	}
}

func TestMeasureWithFakeServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			io.Copy(io.Discard, r.Body)
			return
		}
		buf := make([]byte, 64<<10)
		for i := 0; i < 400; i++ {
			if _, err := w.Write(buf); err != nil {
				return
			}
		}
	}))
	defer srv.Close()
	d, u, err := measureWith(context.Background(), srv.Client(), srv.URL+"/down", srv.URL+"/up", 400*time.Millisecond)
	if err != nil || d <= 0 || u <= 0 {
		t.Fatalf("measureWith = %v %v %v", d, u, err)
	}
	t.Logf("loopback fake: down %.0f Mbit/s up %.0f Mbit/s", d, u)
	if _, _, err := measureWith(context.Background(), srv.Client(), "http://127.0.0.1:1/x", srv.URL, 200*time.Millisecond); err == nil {
		t.Fatal("expected error for dead endpoint")
	}
}

func TestMeasurePathReal(t *testing.T) {
	if testing.Short() {
		t.Skip("real network")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	d, u, err := MeasurePath(ctx, "Wi-Fi", 2*time.Second)
	if err != nil {
		t.Skipf("MeasurePath failed (no network / no Wi-Fi?): %v", err)
	}
	t.Logf("Wi-Fi: download %.1f Mbit/s, upload %.1f Mbit/s", d, u)
}
