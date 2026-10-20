package ui

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mergeinternet/internal/config"
)

func testServer(t *testing.T) (*Server, http.Handler) {
	t.Helper()
	s := newServer(Options{ConfigPath: filepath.Join(t.TempDir(), "client.json"), Logs: NewLogRing(10)}, "secret-token")
	s.port = "7878"
	return s, s.routes()
}

func do(h http.Handler, method, path, host, token, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Host = host
	if token != "" {
		r.Header.Set("X-Token", token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

const host = "127.0.0.1:7878"

func TestStaticPageServedWithSecurityHeaders(t *testing.T) {
	_, h := testServer(t)
	for _, p := range []string{"/", "/app.js", "/style.css"} {
		w := do(h, "GET", p, host, "", "")
		if w.Code != 200 {
			t.Fatalf("%s: status %d", p, w.Code)
		}
		if !strings.Contains(w.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'") {
			t.Fatalf("%s: missing CSP", p)
		}
	}
}

func TestAPIRequiresTokenAndLoopbackHost(t *testing.T) {
	_, h := testServer(t)
	if w := do(h, "GET", "/api/status", host, "", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("no token: %d", w.Code)
	}
	if w := do(h, "GET", "/api/status", host, "wrong", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("wrong token: %d", w.Code)
	}
	// DNS rebinding: a page on evil.example resolving to 127.0.0.1.
	if w := do(h, "GET", "/api/status", "evil.example:7878", "secret-token", ""); w.Code != http.StatusForbidden {
		t.Fatalf("foreign host: %d", w.Code)
	}
	if w := do(h, "GET", "/", "evil.example:7878", "", ""); w.Code != http.StatusForbidden {
		t.Fatalf("foreign host static: %d", w.Code)
	}
	if w := do(h, "GET", "/api/status", "localhost:7878", "secret-token", ""); w.Code != 200 {
		t.Fatalf("localhost: %d", w.Code)
	}
}

func TestConfigRoundTrip(t *testing.T) {
	s, h := testServer(t)
	if w := do(h, "GET", "/api/config", host, s.token, ""); !strings.Contains(w.Body.String(), `"exists":false`) {
		t.Fatalf("missing config: %s", w.Body)
	}
	if w := do(h, "PUT", "/api/config", host, s.token, `{"server":"nope"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("invalid config accepted: %d", w.Code)
	}
	if _, err := os.Stat(s.opts.ConfigPath); err == nil {
		t.Fatal("invalid config was written")
	}
	good := `{"server":"203.0.113.10:51820","key":"mGq3oR1s0Ff6Yb9w2kZ8cX4vN7tL5hP1aD3eQ0uJ6iM=","mode":"redundant",
		"paths":[{"name":"Wi-Fi","interface":"Wi-Fi","up_mbps":40,"down_mbps":100},{"name":"Phone","interface":"Ethernet 3","up_mbps":10,"down_mbps":40}]}`
	if w := do(h, "PUT", "/api/config", host, s.token, good); w.Code != 200 {
		t.Fatalf("save: %d %s", w.Code, w.Body)
	}
	w := do(h, "GET", "/api/status", host, s.token, "")
	var st statusView
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	if !st.Configured || st.Mode != "redundant" || len(st.Links) != 2 || st.Links[1].DnMbps != 40 {
		t.Fatalf("status after save: %+v", st)
	}
}

func TestLinkToggleNeedsRunningTunnel(t *testing.T) {
	s, h := testServer(t)
	if w := do(h, "POST", "/api/links/0", host, s.token, `{"enabled":false}`); w.Code != http.StatusNotFound {
		t.Fatalf("no config: status %d", w.Code)
	}
	do(h, "PUT", "/api/config", host, s.token, baseCfg)
	if w := do(h, "POST", "/api/links/0", host, s.token, `{"enabled":false}`); w.Code != http.StatusConflict {
		t.Fatalf("stopped: status %d", w.Code)
	}
	if w := do(h, "POST", "/api/links/abc", host, s.token, `{}`); w.Code != http.StatusBadRequest {
		t.Fatalf("bad id: %d", w.Code)
	}
}

func TestLogRingKeepsLastLines(t *testing.T) {
	r := NewLogRing(3)
	r.Write([]byte("a\nb\n"))
	r.Write([]byte("c\nd"))
	r.Write([]byte("e\n")) // completes "de"
	got := r.Since(0)
	if len(got) != 3 || got[0].Text != "b" || got[2].Text != "de" {
		t.Fatalf("got %+v", got)
	}
	if more := r.Since(got[2].Seq); len(more) != 0 {
		t.Fatalf("since last: %+v", more)
	}
}

const baseCfg = `{"server":"203.0.113.10:51820","key":"mGq3oR1s0Ff6Yb9w2kZ8cX4vN7tL5hP1aD3eQ0uJ6iM=",
	"paths":[{"name":"LAN","interface":"Ethernet","role":"main","up_mbps":40,"down_mbps":100},
	         {"name":"vivo","interface":"Ethernet 7","role":"extension","up_mbps":10,"down_mbps":40}]}`

func statusOf(t *testing.T, s *Server, h http.Handler) statusView {
	t.Helper()
	var st statusView
	w := do(h, "GET", "/api/status", host, s.token, "")
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil {
		t.Fatalf("status: %v (%s)", err, w.Body)
	}
	return st
}

func TestAddPathEnforcesExtensionLimit(t *testing.T) {
	s, h := testServer(t)
	// No config yet: adding a device creates a local-mode (no VPS) config.
	if w := do(h, "POST", "/api/paths", host, s.token, `{"interface":"Ethernet 8","name":"iPhone","up_mbps":8,"down_mbps":30}`); w.Code != 200 {
		t.Fatalf("add without config: %d %s", w.Code, w.Body)
	}
	if st := statusOf(t, s, h); !st.Configured || st.Connection != "local" || st.ServerSet {
		t.Fatalf("after first add: configured=%v connection=%q serverSet=%v", st.Configured, st.Connection, st.ServerSet)
	}
	if w := do(h, "PUT", "/api/config", host, s.token, baseCfg); w.Code != 200 {
		t.Fatalf("save: %d %s", w.Code, w.Body)
	}
	w := do(h, "POST", "/api/paths", host, s.token, `{"interface":"Ethernet 8","name":"iPhone","device":"iPhone 18 Pro Max","up_mbps":8,"down_mbps":30}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"live":false`) {
		t.Fatalf("add 2nd extension: %d %s", w.Code, w.Body)
	}
	st := statusOf(t, s, h)
	if len(st.Links) != 3 || st.Links[2].Role != "extension" || st.Links[2].Device != "iPhone 18 Pro Max" {
		t.Fatalf("links after add: %+v", st.Links)
	}
	// Fill the remaining extension slots, then one more must be refused.
	for i := 3; i <= config.MaxExtensions; i++ {
		body := fmt.Sprintf(`{"interface":"Ethernet %d","name":"phone %d","up_mbps":5,"down_mbps":20}`, 10+i, i)
		if w := do(h, "POST", "/api/paths", host, s.token, body); w.Code != 200 {
			t.Fatalf("extension %d of %d: %d %s", i, config.MaxExtensions, w.Code, w.Body)
		}
	}
	w = do(h, "POST", "/api/paths", host, s.token, `{"interface":"Wi-Fi","name":"Hotspot","up_mbps":5,"down_mbps":20}`)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), fmt.Sprintf("tối đa %d thiết bị", config.MaxExtensions)) {
		t.Fatalf("extension %d: %d %s", config.MaxExtensions+1, w.Code, w.Body)
	}
	if w := do(h, "POST", "/api/paths", host, s.token, `{"interface":"Ethernet 7","name":"dup","up_mbps":1,"down_mbps":1}`); w.Code != http.StatusBadRequest {
		t.Fatalf("duplicate interface: %d", w.Code)
	}
}

func TestLimitsPersistWhileStoppedAndQuotaCaps(t *testing.T) {
	s, h := testServer(t)
	do(h, "PUT", "/api/config", host, s.token, baseCfg)
	if w := do(h, "POST", "/api/links/1", host, s.token, `{"limitMbps":16,"quotaGB":0.001}`); w.Code != 200 {
		t.Fatalf("set limits: %d %s", w.Code, w.Body)
	}
	cfg, err := config.LoadClient(s.opts.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Paths[1].LimitMbps != 16 || cfg.Paths[1].QuotaGB != 0.001 {
		t.Fatalf("not persisted: %+v", cfg.Paths[1])
	}
	// 2 MB used on a 1 MB quota: the device shows as capped even while stopped.
	s.usage.Add("Ethernet 7", 2_000_000)
	st := statusOf(t, s, h)
	if !st.Links[1].Capped || st.Links[1].UsageBytes != 2_000_000 || st.Links[1].LimitMbps != 16 {
		t.Fatalf("link 1: %+v", st.Links[1])
	}
	if w := do(h, "POST", "/api/links/1/reset-usage", host, s.token, ""); w.Code != 200 {
		t.Fatalf("reset: %d %s", w.Code, w.Body)
	}
	if st := statusOf(t, s, h); st.Links[1].Capped || st.Links[1].UsageBytes != 0 {
		t.Fatalf("after reset: %+v", st.Links[1])
	}
	if w := do(h, "POST", "/api/links/1", host, s.token, `{"enabled":false}`); w.Code != http.StatusConflict {
		t.Fatalf("toggle while stopped: %d", w.Code)
	}
	if w := do(h, "POST", "/api/links/9", host, s.token, `{"limitMbps":1}`); w.Code != http.StatusNotFound {
		t.Fatalf("unknown link: %d", w.Code)
	}
	if w := do(h, "POST", "/api/links/1", host, s.token, `{"limitMbps":-1}`); w.Code != http.StatusBadRequest {
		t.Fatalf("negative limit: %d", w.Code)
	}
}
