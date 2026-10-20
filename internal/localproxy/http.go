package localproxy

import (
	"io"
	"net/http"
	"strings"
	"time"
)

var hopHeaders = []string{
	"Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization",
	"Proxy-Connection", "Te", "Trailer", "Transfer-Encoding", "Upgrade",
}

func stripHop(h http.Header) {
	for _, v := range h.Values("Connection") {
		for _, tok := range strings.Split(v, ",") {
			if tok = strings.TrimSpace(tok); tok != "" {
				h.Del(tok)
			}
		}
	}
	for _, k := range hopHeaders {
		h.Del(k)
	}
	for k := range h {
		if strings.HasPrefix(strings.ToLower(k), "proxy-") {
			delete(h, k)
		}
	}
}

func (p *Proxy) serveHTTP(w http.ResponseWriter, r *http.Request) {
	if !p.begin() {
		http.Error(w, "stopping", http.StatusServiceUnavailable)
		return
	}
	defer p.wg.Done()
	switch {
	case r.Method == http.MethodConnect:
		p.handleConnect(w, r)
	case r.URL.IsAbs() && r.URL.Scheme == "http" && r.URL.Host != "":
		p.handleForward(w, r)
	default:
		http.Error(w, "this is a proxy: send CONNECT or an absolute-URI request", http.StatusBadRequest)
	}
}

func (p *Proxy) handleConnect(w http.ResponseWriter, r *http.Request) {
	target := r.Host
	if target == "" {
		target = r.URL.Host
	}
	if !strings.Contains(target, ":") || strings.HasSuffix(target, "]") {
		http.Error(w, "CONNECT needs host:port", http.StatusBadRequest)
		return
	}
	if refuseDest(target) {
		http.Error(w, "destination not allowed", http.StatusForbidden)
		return
	}
	up, err := p.dialBalanced(r.Context(), "tcp", target)
	if err != nil {
		p.logf("CONNECT %s: %v", target, err)
		http.Error(w, "upstream dial failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		up.Close()
		http.Error(w, "hijack unsupported", http.StatusInternalServerError)
		return
	}
	conn, brw, err := hj.Hijack()
	if err != nil {
		up.Close()
		return
	}
	untrack, ok := p.track(conn)
	if !ok {
		up.Close()
		return
	}
	defer untrack()
	conn.SetDeadline(time.Time{})
	conn.SetWriteDeadline(time.Now().Add(handshakeLimit))
	if _, err := conn.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n")); err != nil {
		conn.Close()
		up.Close()
		return
	}
	conn.SetWriteDeadline(time.Time{})
	var pre []byte
	if n := brw.Reader.Buffered(); n > 0 {
		pre = make([]byte, n)
		io.ReadFull(brw.Reader, pre)
	}
	tunnel(conn, up, pre)
}

func (p *Proxy) handleForward(w http.ResponseWriter, r *http.Request) {
	if refuseDest(r.URL.Host) {
		http.Error(w, "destination not allowed", http.StatusForbidden)
		return
	}
	out := r.Clone(r.Context())
	out.RequestURI = ""
	stripHop(out.Header)
	if _, ok := out.Header["User-Agent"]; !ok {
		out.Header.Set("User-Agent", "") // stop net/http injecting its own
	}
	resp, err := p.tr.RoundTrip(out)
	if err != nil {
		p.logf("GET %s: %v", r.URL.Host, err)
		http.Error(w, "upstream request failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	stripHop(resp.Header)
	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	fl, _ := w.(http.Flusher)
	buf := make([]byte, copyChunk)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			if fl != nil {
				fl.Flush()
			}
		}
		if err != nil {
			return
		}
	}
}
