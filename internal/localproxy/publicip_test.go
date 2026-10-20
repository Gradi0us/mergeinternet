package localproxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

func TestPublicIPParsesTrace(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok":
			w.Write([]byte("fl=123\nh=speed.cloudflare.com\nip=203.0.113.7\nts=1.2\n"))
		case "/noip":
			w.Write([]byte("fl=123\nh=x\n"))
		default:
			http.Error(w, "nope", http.StatusForbidden)
		}
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if ip, err := publicIPWith(ctx, srv.Client(), srv.URL+"/ok"); err != nil || ip != "203.0.113.7" {
		t.Fatalf("ok: ip=%q err=%v", ip, err)
	}
	if _, err := publicIPWith(ctx, srv.Client(), srv.URL+"/noip"); err == nil {
		t.Fatal("trace without ip= accepted")
	}
	if _, err := publicIPWith(ctx, srv.Client(), srv.URL+"/bad"); err == nil {
		t.Fatal("HTTP 403 accepted")
	}
}

// Real network check, opt-in: MI_REAL_IFACE=Ethernet go test -run RealPublicIP ./internal/localproxy
func TestRealPublicIP(t *testing.T) {
	iface := os.Getenv("MI_REAL_IFACE")
	if iface == "" {
		t.Skip("set MI_REAL_IFACE to an adapter name to query its public IP")
	}
	ip, err := PublicIP(context.Background(), iface)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%s -> %s", iface, ip)
}
