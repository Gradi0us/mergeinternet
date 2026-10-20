package localproxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"mergeinternet/internal/bindif"
)

var (
	measureDownURL = "https://speed.cloudflare.com/__down?bytes=50000000"
	measureUpURL   = "https://speed.cloudflare.com/__up"
	publicIPURL    = "https://speed.cloudflare.com/cdn-cgi/trace"
)

const (
	measureStreams  = 4
	measureWarmup   = 500 * time.Millisecond
	publicIPTimeout = 8 * time.Second
)

// MeasurePath measures one adapter's download and upload capacity against a
// public speed-test endpoint, with traffic pinned to that interface. Each
// direction runs about dur (after a short warm-up) over several parallel
// streams so one TCP flow's slow start does not under-measure.
func MeasurePath(ctx context.Context, iface string, dur time.Duration) (downMbps, upMbps float64, err error) {
	cl, done, err := pinnedClient(iface)
	if err != nil {
		return 0, 0, err
	}
	defer done()
	return measureWith(ctx, cl, measureDownURL, measureUpURL, dur)
}

// PublicIP reports the public IPv4 address this adapter reaches the internet
// with. Two adapters with the same public IP share one uplink (for example
// LAN and Wi-Fi of the same home router), so using both adds no speed.
func PublicIP(ctx context.Context, iface string) (string, error) {
	cl, done, err := pinnedClient(iface)
	if err != nil {
		return "", err
	}
	defer done()
	ctx, cancel := context.WithTimeout(ctx, publicIPTimeout)
	defer cancel()
	return publicIPWith(ctx, cl, publicIPURL)
}

func publicIPWith(ctx context.Context, cl *http.Client, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := cl.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("public ip: HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return "", err
	}
	// Cloudflare's trace is "key=value" lines, one of them "ip=203.0.113.7".
	for _, line := range strings.Split(string(body), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "ip="); ok {
			if a, err := netip.ParseAddr(v); err == nil {
				return a.String(), nil
			}
		}
	}
	return "", errors.New("public ip: no ip= line in the trace response")
}

// pinnedClient is an HTTP client whose connections all leave through iface.
func pinnedClient(iface string) (*http.Client, func(), error) {
	d, _, err := bindif.DialerFor(iface)
	if err != nil {
		return nil, nil, err
	}
	d.Timeout = dialTimeout
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
			return d.DialContext(ctx, "tcp4", addr)
		},
		DisableCompression:  true,
		TLSHandshakeTimeout: 10 * time.Second,
		MaxIdleConnsPerHost: measureStreams,
	}
	return &http.Client{Transport: tr}, tr.CloseIdleConnections, nil
}

func measureWith(ctx context.Context, cl *http.Client, downURL, upURL string, dur time.Duration) (down, up float64, err error) {
	if dur <= 0 {
		dur = 3 * time.Second
	}
	down, err = runDirection(ctx, dur, func(ctx context.Context, n *atomic.Int64) error {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, downURL, nil)
		if err != nil {
			return err
		}
		resp, err := cl.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			return fmt.Errorf("download: HTTP %d", resp.StatusCode)
		}
		buf := make([]byte, 64*1024)
		for {
			k, err := resp.Body.Read(buf)
			n.Add(int64(k))
			if err != nil {
				if err == io.EOF {
					return nil
				}
				return err
			}
		}
	})
	if err != nil {
		return 0, 0, fmt.Errorf("download test: %w", err)
	}
	up, err = runDirection(ctx, dur, func(ctx context.Context, n *atomic.Int64) error {
		const size = 20 << 20
		body := &countingZeros{left: size, n: n}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, upURL, body)
		if err != nil {
			return err
		}
		req.ContentLength = size
		req.Header.Set("Content-Type", "application/octet-stream")
		resp, err := cl.Do(req)
		if err != nil {
			return err
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode >= 300 {
			return fmt.Errorf("upload: HTTP %d", resp.StatusCode)
		}
		return nil
	})
	if err != nil {
		return down, 0, fmt.Errorf("upload test: %w", err)
	}
	return down, up, nil
}

type countingZeros struct {
	left int
	n    *atomic.Int64
}

func (c *countingZeros) Read(b []byte) (int, error) {
	if c.left <= 0 {
		return 0, io.EOF
	}
	if len(b) > c.left {
		b = b[:c.left]
	}
	clear(b)
	c.left -= len(b)
	c.n.Add(int64(len(b)))
	return len(b), nil
}

// runDirection runs measureStreams workers repeating fn until warm-up+dur has
// passed, and returns Mbps between the end of the warm-up and the end.
func runDirection(ctx context.Context, dur time.Duration, fn func(context.Context, *atomic.Int64) error) (float64, error) {
	rctx, cancel := context.WithTimeout(ctx, measureWarmup+dur)
	defer cancel()
	var (
		total atomic.Int64
		wg    sync.WaitGroup
		mu    sync.Mutex
		first error
	)
	for i := 0; i < measureStreams; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for rctx.Err() == nil {
				if err := fn(rctx, &total); err != nil && rctx.Err() == nil {
					mu.Lock()
					if first == nil {
						first = err
					}
					mu.Unlock()
					return
				}
			}
		}()
	}
	start := time.Now()
	select {
	case <-time.After(measureWarmup):
	case <-rctx.Done():
	}
	b0, t0 := total.Load(), time.Now()
	<-rctx.Done()
	b1, t1 := total.Load(), time.Now()
	cancel()
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	_ = start
	if b1 == 0 || b1 == b0 {
		mu.Lock()
		defer mu.Unlock()
		if first != nil {
			return 0, first
		}
		return 0, errors.New("no data transferred")
	}
	return float64(b1-b0) * 8 / t1.Sub(t0).Seconds() / 1e6, nil
}
