// Package ui serves the local web dashboard of mi-client: one-click setup,
// start/stop, live per-device speeds, limits and quotas, settings and logs.
// It listens on 127.0.0.1 only and every API call must carry a random
// per-run token.
package ui

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"time"

	"mergeinternet/internal/client"
	"mergeinternet/internal/config"
	"mergeinternet/internal/devinfo"
	"mergeinternet/internal/sysproxy"
)

//go:embed web
var webFS embed.FS

type Options struct {
	ConfigPath string
	Addr       string // e.g. 127.0.0.1:7878
	Logs       *LogRing
	// OpenBrowser is called with the dashboard URL once the server listens.
	OpenBrowser func(url string)
	// AutoStart starts the connection right away (used after an elevation relaunch).
	AutoStart bool
	// Takeover waits for the previous copy to release the dashboard port
	// (elevation relaunch) instead of handing over to it.
	Takeover bool
	// Guard starts a watchdog process that restores the system proxy if this
	// process dies without cleaning up (killed, crashed).
	Guard bool
}

type Server struct {
	opts    Options
	token   string
	port    string
	usage   *client.Usage
	devices *devinfo.Watcher
	sys     *sysproxy.Manager
	baseCtx context.Context

	mu      sync.Mutex
	runner  client.Runner
	phase   string // stopped | starting | running | stopping
	lastErr string
	smp     sampler

	cfgMu  sync.Mutex // serializes read-modify-write of the config file
	syncMu sync.Mutex // serializes hot-adds so a path is never added twice
	jobs   jobs
}

const (
	phaseStopped  = "stopped"
	phaseStarting = "starting"
	phaseRunning  = "running"
	phaseStopping = "stopping"
)

// errNeedAdmin tells the UI to relaunch elevated (VPS mode needs a TUN device).
var errNeedAdmin = errors.New("need_admin")

func newServer(opts Options, token string) *Server {
	return &Server{
		opts:    opts,
		token:   token,
		phase:   phaseStopped,
		usage:   client.LoadUsage(client.UsagePathFor(opts.ConfigPath)),
		devices: devinfo.NewWatcher(3 * time.Second),
		sys:     sysproxy.New(sysproxy.DefaultBackupPath()),
		baseCtx: context.Background(),
	}
}

// Run serves the dashboard until ctx is done, then stops the connection.
// If another copy already serves the dashboard, it asks that copy to open
// the browser and returns ErrAlreadyRunning.
func Run(ctx context.Context, opts Options) error {
	tok := make([]byte, 16)
	if _, err := rand.Read(tok); err != nil {
		return err
	}
	s := newServer(opts, hex.EncodeToString(tok))
	s.baseCtx = ctx

	ln, err := net.Listen("tcp", opts.Addr)
	for i := 0; err != nil && opts.Takeover && i < 25; i++ {
		time.Sleep(200 * time.Millisecond) // the copy that relaunched us is exiting
		ln, err = net.Listen("tcp", opts.Addr)
	}
	if err != nil {
		if !opts.Takeover && askRunningCopyToOpen(opts.Addr) {
			return ErrAlreadyRunning
		}
		// Port taken by something else: fall back to any free port.
		if ln, err = net.Listen("tcp", "127.0.0.1:0"); err != nil {
			return err
		}
	}
	_, s.port, _ = net.SplitHostPort(ln.Addr().String())

	// A previous session that crashed may have left the system proxy pointing at us.
	if did, err := s.sys.Recover(); err != nil {
		log.Printf("restore system proxy: %v", err)
	} else if did {
		log.Printf("đã khôi phục proxy hệ thống từ phiên trước")
	}
	if opts.Guard {
		if err := startGuard(s.sys.BackupPath); err != nil {
			log.Printf("guard: %v (proxy is restored on next launch instead)", err)
		}
	}

	srv := &http.Server{Handler: s.routes(), ReadHeaderTimeout: 10 * time.Second}
	go s.devices.Run(ctx)
	go s.sampleLoop(ctx)
	go s.autoLoop(ctx)
	go func() {
		<-ctx.Done()
		s.stopRunner()
		if err := s.usage.Save(); err != nil {
			log.Printf("save usage: %v", err)
		}
		shut, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		srv.Shutdown(shut)
	}()

	// The token stays out of the log (users paste logs into support requests).
	log.Printf("dashboard: http://127.0.0.1:%s/", s.port)
	fmt.Printf("Mở bảng điều khiển: %s\n", s.dashboardURL())
	if opts.OpenBrowser != nil {
		opts.OpenBrowser(s.dashboardURL())
	}
	if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// ErrAlreadyRunning: another copy owns the dashboard; it was asked to open it.
var ErrAlreadyRunning = errors.New("mi-client đang chạy; đã mở lại bảng điều khiển")

func (s *Server) dashboardURL() string {
	return fmt.Sprintf("http://127.0.0.1:%s/#token=%s", s.port, s.token)
}

// askRunningCopyToOpen pokes an already running copy so double-clicking the
// exe again simply brings the dashboard back.
func askRunningCopyToOpen(addr string) bool {
	req, err := http.NewRequest(http.MethodPost, "http://"+addr+"/open", nil)
	if err != nil {
		return false
	}
	req.Header.Set("X-MI-Open", "1")
	c := http.Client{Timeout: 3 * time.Second}
	resp, err := c.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusNoContent
}

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	static, _ := fs.Sub(webFS, "web")
	mux.Handle("GET /", http.FileServerFS(static))
	// No token: it only makes this process open its own dashboard. The custom
	// header cannot be sent cross-origin without a CORS preflight, which we never answer.
	mux.HandleFunc("POST /open", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-MI-Open") != "1" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if s.opts.OpenBrowser != nil {
			s.opts.OpenBrowser(s.dashboardURL())
		} else {
			OpenBrowser(s.dashboardURL())
		}
		w.WriteHeader(http.StatusNoContent)
	})

	for pattern, h := range map[string]http.HandlerFunc{
		"GET /api/status":                  s.handleStatus,
		"GET /api/config":                  s.handleGetConfig,
		"PUT /api/config":                  s.handlePutConfig,
		"GET /api/devices":                 s.handleDevices,
		"POST /api/devices/refresh":        s.handleDevicesRefresh,
		"POST /api/start":                  s.handleStart,
		"POST /api/stop":                   s.handleStop,
		"POST /api/paths":                  s.handleAddPath,
		"POST /api/links/{id}":             s.handleLink,
		"POST /api/links/{id}/reset-usage": s.handleResetUsage,
		"POST /api/measure":                s.handleMeasure,
		"POST /api/setup/local":            s.handleSetupLocal,
		"POST /api/setup/vps":              s.handleSetupVPS,
		"POST /api/setup/code":             s.handleSetupCode,
		"GET /api/prefs":                   s.handleGetPrefs,
		"PUT /api/prefs":                   s.handlePutPrefs,
		"GET /api/logs":                    s.handleLogs,
		"POST /api/elevate":                s.handleElevate,
	} {
		mux.Handle(pattern, s.guard(h))
	}
	return s.checkHost(mux)
}

// checkHost blocks DNS-rebinding: only requests addressed to our loopback port are served.
func (s *Server) checkHost(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "127.0.0.1:"+s.port && r.Host != "localhost:"+s.port {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}

// guard requires the per-run token, which only the URL we opened knows.
func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Token")), []byte(s.token)) != 1 {
			writeErr(w, http.StatusUnauthorized, "Phiên làm việc đã hết hạn. Hãy mở lại mi-client.exe.")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) sampleLoop(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			if r := s.running(); r != nil {
				paths := r.Paths()
				s.mu.Lock()
				s.smp.add(now, paths)
				s.mu.Unlock()
			}
		}
	}
}

// ---- start / stop ----

func (s *Server) handleStart(w http.ResponseWriter, r *http.Request) {
	if err := s.startRunner(); err != nil {
		if errors.Is(err, errNeedAdmin) {
			writeJSON(w, map[string]any{"needAdmin": true})
			return
		}
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, map[string]string{"phase": phaseStarting})
}

// startRunner starts the configured connection mode in the background.
func (s *Server) startRunner() error {
	cfg, err := config.LoadClient(s.opts.ConfigPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, config.ErrNoPaths) {
			return errors.New("Chưa có cấu hình: bấm “Bắt đầu ngay” để app tự thiết lập.")
		}
		return errors.New("Cấu hình chưa hợp lệ: " + err.Error())
	}
	if cfg.Connection == config.ConnVPS {
		if runtime.GOOS == "windows" && !isElevated() {
			return errNeedAdmin
		}
		if !wintunPresent() {
			return errors.New("Thiếu wintun.dll cạnh mi-client.exe. Chạy scripts\\get-wintun.ps1.")
		}
	}
	s.mu.Lock()
	if s.phase != phaseStopped {
		s.mu.Unlock()
		return errors.New("Kết nối đang chạy hoặc đang khởi động.")
	}
	s.phase, s.lastErr = phaseStarting, ""
	s.smp.reset()
	s.mu.Unlock()

	go func() {
		var r client.Runner
		var err error
		if cfg.Connection == config.ConnVPS {
			r, err = client.Start(cfg, s.usage)
		} else {
			r, err = client.StartLocal(cfg, s.usage, s.sys)
		}
		s.mu.Lock()
		if err != nil {
			s.phase, s.lastErr = phaseStopped, err.Error()
			s.mu.Unlock()
			log.Printf("start failed: %v", err)
			return
		}
		s.runner, s.phase = r, phaseRunning
		s.mu.Unlock()
		s.syncPaths(r)        // paths saved while we were starting
		s.measureUnmeasured() // first run: replace placeholder weights with real ones

		<-r.Done()
		s.mu.Lock()
		if s.runner == r {
			s.runner, s.phase = nil, phaseStopped
			if err := r.Err(); err != nil {
				s.lastErr = err.Error()
			}
		}
		s.mu.Unlock()
	}()
	return nil
}

func (s *Server) handleStop(w http.ResponseWriter, r *http.Request) {
	s.stopRunner()
	writeJSON(w, map[string]string{"phase": phaseStopped})
}

func (s *Server) stopRunner() {
	s.mu.Lock()
	r := s.runner
	if r == nil {
		s.mu.Unlock()
		return
	}
	s.phase = phaseStopping
	s.mu.Unlock()
	r.Stop() // the start goroutine observes Done and resets state
	<-r.Done()
	s.mu.Lock()
	if s.runner == r {
		s.runner, s.phase = nil, phaseStopped
	}
	s.mu.Unlock()
}

func (s *Server) running() client.Runner {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.runner
}

// hotAdd adds p to the running connection unless it already has that
// interface, and returns the path's ID either way.
func (s *Server) hotAdd(r client.Runner, p config.Path) (uint8, error) {
	s.syncMu.Lock()
	defer s.syncMu.Unlock()
	for _, pi := range r.Paths() {
		if pi.Path.Interface == p.Interface {
			return pi.ID, nil
		}
	}
	return r.AddPath(p)
}

// syncPaths hot-adds configured paths the connection does not have yet. The
// connection starts from the config read at startup, so a phone auto-added
// while it was still starting would otherwise be missing until a restart.
func (s *Server) syncPaths(r client.Runner) {
	cfg, err := config.LoadClient(s.opts.ConfigPath)
	if err != nil {
		return
	}
	for _, p := range cfg.Paths {
		if _, err := s.hotAdd(r, p); err != nil {
			log.Printf("thêm %q vào kết nối: %v", p.Name, err)
		}
	}
}

// ---- logs / elevate ----

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	since, _ := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)
	writeJSON(w, s.opts.Logs.Since(since))
}

// handleElevate relaunches with admin rights; with ?start=1 the new copy
// connects right away, so "Bật kết nối" stays a single click.
func (s *Server) handleElevate(w http.ResponseWriter, r *http.Request) {
	autoStart := r.URL.Query().Get("start") == "1"
	s.stopRunner() // the elevated copy takes over (and the dashboard port)
	if err := relaunchElevated(s.opts.ConfigPath, autoStart); err != nil {
		writeErr(w, http.StatusInternalServerError, "Không mở lại được với quyền Admin: "+err.Error())
		return
	}
	writeJSON(w, map[string]bool{"relaunched": true})
	go func() {
		time.Sleep(500 * time.Millisecond) // let the response reach the browser
		os.Exit(0)
	}()
}

// ---- helpers ----

func wintunPresent() bool {
	if runtime.GOOS != "windows" {
		return true
	}
	exe, err := os.Executable()
	if err != nil {
		return false
	}
	_, err = os.Stat(filepath.Join(filepath.Dir(exe), "wintun.dll"))
	return err == nil
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
