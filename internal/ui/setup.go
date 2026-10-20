package ui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"mergeinternet/internal/client"
	"mergeinternet/internal/config"
	"mergeinternet/internal/devinfo"
	"mergeinternet/internal/localproxy"
	"mergeinternet/internal/provision"
)

// ---- background jobs (setup, measurement) shown as a step list in the UI ----

type jobStep struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Status string `json:"status"` // running | done | failed | skipped
	Detail string `json:"detail"`
}

type jobView struct {
	Kind     string    `json:"kind"` // local | vps | code | measure
	Running  bool      `json:"running"`
	Steps    []jobStep `json:"steps"`
	Error    string    `json:"error"`
	Message  string    `json:"message"`
	NeedsAdm bool      `json:"needAdmin"`
	Quiet    bool      `json:"quiet"` // background job the UI shows discreetly
	Ended    int64     `json:"ended"`
}

type jobs struct {
	mu  sync.Mutex
	cur *jobView
}

// begin starts a job unless one is running.
func (j *jobs) begin(kind string, quiet bool) bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.cur != nil && j.cur.Running {
		return false
	}
	j.cur = &jobView{Kind: kind, Running: true, Quiet: quiet}
	return true
}

func (j *jobs) step(id, title, status, detail string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.cur == nil {
		return
	}
	for i := range j.cur.Steps {
		if j.cur.Steps[i].ID == id {
			if title != "" {
				j.cur.Steps[i].Title = title
			}
			j.cur.Steps[i].Status, j.cur.Steps[i].Detail = status, detail
			return
		}
	}
	j.cur.Steps = append(j.cur.Steps, jobStep{ID: id, Title: title, Status: status, Detail: detail})
}

func (j *jobs) finish(err error, msg string, needAdmin bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.cur == nil {
		return
	}
	j.cur.Running, j.cur.Message, j.cur.NeedsAdm, j.cur.Ended = false, msg, needAdmin, time.Now().UnixMilli()
	if err != nil {
		j.cur.Error = err.Error()
	}
}

func (j *jobs) running() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.cur != nil && j.cur.Running
}

func (j *jobs) view() *jobView {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.cur == nil {
		return nil
	}
	c := *j.cur
	c.Steps = append([]jobStep(nil), j.cur.Steps...)
	return &c
}

func (s *Server) busy(w http.ResponseWriter) {
	writeErr(w, http.StatusConflict, "Đang có một tác vụ khác chạy, đợi nó xong đã.")
}

// ---- choosing paths automatically ----

// autoPaths picks the main line and up to MaxExtensions phones from what is
// plugged in right now.
func autoPaths(devs []devinfo.Device, ignored func(string) bool) []config.Path {
	var out []config.Path
	hasMain, exts := false, 0
	for _, d := range devs { // devinfo sorts main candidates first
		if !d.Usable || d.Role == devinfo.RoleIgnore || ignored(d.Interface) {
			continue
		}
		role := ""
		switch {
		case d.Role == devinfo.RoleMain && !hasMain:
			role, hasMain = config.RoleMain, true
		case d.Role == devinfo.RoleExtension && exts < config.MaxExtensions:
			role = config.RoleExtension
			exts++
		default:
			continue
		}
		down, up := config.DefaultSpeeds(role)
		p := config.Path{Name: d.Name, Interface: d.Interface, Role: role, DownMbps: down, UpMbps: up}
		if role == config.RoleExtension {
			p.Device = d.Name
		}
		out = append(out, p)
	}
	return out
}

func describe(paths []config.Path) string {
	main, exts := "", 0
	for _, p := range paths {
		if p.Role == config.RoleMain {
			main = p.Name
		} else {
			exts++
		}
	}
	if main == "" {
		return fmt.Sprintf("%d thiết bị mở rộng", exts)
	}
	return fmt.Sprintf("%s + %d thiết bị mở rộng", main, exts)
}

// detectStep refreshes device detection and fills in paths when the config has none.
func (s *Server) detectStep(ctx context.Context) error {
	s.jobs.step("devices", "Tìm các nguồn mạng đang có", "running", "")
	dctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	devs := s.devices.Refresh(dctx)
	cancel()
	var chosen []config.Path
	err := s.updateConfig(func(c *config.Client) error {
		if len(c.Paths) == 0 {
			c.Paths = autoPaths(devs, c.IsIgnored)
		}
		if len(c.Paths) == 0 {
			return errors.New("Không tìm thấy mạng nào có internet. Hãy kết nối Wi-Fi/LAN hoặc cắm điện thoại rồi thử lại.")
		}
		chosen = c.Paths
		return nil
	})
	if err != nil {
		s.jobs.step("devices", "", "failed", err.Error())
		return err
	}
	s.jobs.step("devices", "", "done", describe(chosen))
	return nil
}

// ---- measurement ----

// measureStep measures paths (all when ids is nil) and stores the results as
// weights. With a running connection it measures through it; otherwise it
// measures directly (VPS mode) or against a public speed test (local mode).
func (s *Server) measureStep(ctx context.Context, ids []int) {
	s.jobs.step("measure", "Đo tốc độ từng đường", "running", "Mất khoảng 10 giây…")
	type target struct {
		id   uint8
		path config.Path
	}
	var targets []target
	run := s.running()
	cfg, _ := s.loadAny()
	if run != nil {
		for _, pi := range run.Paths() {
			if pi.Enabled && wanted(ids, int(pi.ID)) {
				targets = append(targets, target{pi.ID, pi.Path})
			}
		}
	} else if cfg != nil {
		for i, p := range cfg.Paths {
			if wanted(ids, i) {
				targets = append(targets, target{uint8(i), p})
			}
		}
	}
	if len(targets) == 0 {
		s.jobs.step("measure", "", "skipped", "Không có đường nào để đo.")
		return
	}

	progress := func(r client.MeasureResult) {
		t := targets[0]
		for _, x := range targets {
			if x.path.Interface == r.Interface {
				t = x
			}
		}
		id := fmt.Sprintf("m%d", t.id)
		name := t.path.Name
		if t.path.Device != "" {
			name = t.path.Device
		}
		if r.Err != nil {
			s.jobs.step(id, "Đo "+name, "failed", r.Err.Error()+" (giữ tốc độ ước lượng)")
			return
		}
		s.jobs.step(id, "Đo "+name, "done", fmt.Sprintf("↓ %.0f Mbps · ↑ %.0f Mbps", r.DownMbps, r.UpMbps))
	}
	for _, t := range targets {
		name := t.path.Name
		if t.path.Device != "" {
			name = t.path.Device
		}
		s.jobs.step(fmt.Sprintf("m%d", t.id), "Đo "+name, "running", "")
	}

	mctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	all := make([]config.Path, 0, len(targets))
	if cfg != nil {
		all = append(all, cfg.Paths...)
	} else {
		for _, t := range targets {
			all = append(all, t.path)
		}
	}
	ips := s.lookupPublicIPs(mctx, all)
	ids8 := make([]uint8, len(targets))
	paths := make([]config.Path, len(targets))
	for i, t := range targets {
		ids8[i], paths[i] = t.id, t.path
	}
	var results []client.MeasureResult
	switch {
	case run != nil:
		results = run.Measure(mctx, ids8, client.MeasureDuration, progress)
	case cfg != nil && cfg.Connection == config.ConnVPS:
		results = client.MeasureVPS(mctx, cfg, paths, client.MeasureDuration, progress)
	default:
		results = client.MeasureLocal(mctx, paths, client.MeasureDuration, progress)
	}

	ok := 0
	now := time.Now().Format(time.RFC3339)
	_ = s.updateConfig(func(c *config.Client) error {
		for j := range c.Paths {
			if ip, ok := ips[c.Paths[j].Interface]; ok {
				c.Paths[j].PublicIP = ip
			}
		}
		for i, r := range results {
			if r.Err != nil {
				continue
			}
			down, up := r.Weights()
			for j := range c.Paths {
				if c.Paths[j].Interface == paths[i].Interface {
					c.Paths[j].DownMbps, c.Paths[j].UpMbps, c.Paths[j].MeasuredAt = down, up, now
				}
			}
			if run != nil {
				_ = run.SetWeights(ids8[i], down, up)
			}
			ok++
		}
		return nil
	})
	for _, r := range results {
		if r.Err != nil {
			s.markTried(paths, r.Interface)
		}
	}
	// A LAN / Wi-Fi line that leaves through the main line's uplink adds no speed.
	mainName, mainIP := "", ""
	for _, p := range all {
		if p.Role == config.RoleMain {
			mainName, mainIP = displayName(p), ips[p.Interface]
		}
	}
	for i, t := range targets {
		if t.path.Role == config.RoleMain || mainIP == "" || ips[t.path.Interface] != mainIP || results[i].Err != nil {
			continue
		}
		r := results[i]
		s.jobs.step(fmt.Sprintf("m%d", t.id), "Đo "+displayName(t.path), "done",
			fmt.Sprintf("↓ %.0f Mbps · ↑ %.0f Mbps · cùng đường internet với %s (IP %s): không cộng thêm tốc độ", r.DownMbps, r.UpMbps, mainName, mainIP))
		log.Printf("%q dùng chung đường internet với %q (IP công cộng %s): không tăng tốc", displayName(t.path), mainName, mainIP)
	}
	status := "done"
	if ok == 0 {
		status = "failed"
	}
	s.jobs.step("measure", "", status, fmt.Sprintf("Đã đo %d/%d đường", ok, len(results)))
}

func displayName(p config.Path) string {
	if p.Device != "" {
		return p.Device
	}
	return p.Name
}

// lookupPublicIPs asks which public IP each LAN / Wi-Fi line reaches the
// internet with. Phones are skipped: mobile carriers put many subscribers
// behind one public IP (CGNAT), so equal IPs there do not mean a shared line.
func (s *Server) lookupPublicIPs(ctx context.Context, paths []config.Path) map[string]string {
	via := map[string]devinfo.Via{}
	for _, d := range s.devices.Snapshot() {
		via[d.Interface] = d.Via
	}
	out := map[string]string{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, p := range paths {
		if v := via[p.Interface]; v == devinfo.ViaUSB || v == devinfo.ViaHotspot {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			ip, err := localproxy.PublicIP(ctx, p.Interface)
			if err != nil {
				log.Printf("public IP of %q: %v", p.Interface, err)
				return
			}
			mu.Lock()
			out[p.Interface] = ip
			mu.Unlock()
		}()
	}
	wg.Wait()
	return out
}

func wanted(ids []int, id int) bool {
	if ids == nil {
		return true
	}
	for _, x := range ids {
		if x == id {
			return true
		}
	}
	return false
}

func (s *Server) handleMeasure(w http.ResponseWriter, r *http.Request) {
	var body struct {
		IDs []int `json:"ids"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 1<<10)).Decode(&body)
	if !s.jobs.begin("measure", false) {
		s.busy(w)
		return
	}
	go func() {
		s.measureStep(s.baseCtx, body.IDs)
		s.jobs.finish(nil, "Đã cập nhật tốc độ các đường.", false)
	}()
	writeJSON(w, map[string]bool{"started": true})
}

// measureUnmeasured measures, in the background, running paths that still use
// placeholder speeds (first start, phone just added). Failed paths are not
// retried for a while so a dead phone does not get flooded every few seconds.
func (s *Server) measureUnmeasured() {
	run := s.running()
	if run == nil || s.jobs.running() {
		return
	}
	var ids []int
	for _, pi := range run.Paths() {
		if pi.Path.MeasuredAt == "" && pi.Enabled && pi.Alive && !s.recentlyTried(pi.Path.Interface) {
			ids = append(ids, int(pi.ID))
		}
	}
	if len(ids) == 0 || !s.jobs.begin("measure", true) {
		return
	}
	go func() {
		s.measureStep(s.baseCtx, ids)
		s.jobs.finish(nil, "Đã đo tốc độ đường mới.", false)
	}()
}

var (
	triedMu sync.Mutex
	tried   = map[string]time.Time{}
)

const measureRetryAfter = 10 * time.Minute

func (s *Server) markTried(_ []config.Path, iface string) {
	triedMu.Lock()
	tried[iface] = time.Now()
	triedMu.Unlock()
}

func (s *Server) recentlyTried(iface string) bool {
	triedMu.Lock()
	defer triedMu.Unlock()
	t, ok := tried[iface]
	return ok && time.Since(t) < measureRetryAfter
}

// ---- one-click setups ----

// handleSetupLocal: "Bắt đầu ngay" - no VPS. Detect, measure, connect.
func (s *Server) handleSetupLocal(w http.ResponseWriter, r *http.Request) {
	if !s.jobs.begin("local", false) {
		s.busy(w)
		return
	}
	go func() {
		ctx := s.baseCtx
		if err := s.updateConfig(func(c *config.Client) error { c.Connection = config.ConnLocal; return nil }); err != nil {
			s.jobs.finish(err, "", false)
			return
		}
		if err := s.detectStep(ctx); err != nil {
			s.jobs.finish(err, "", false)
			return
		}
		s.stopRunner()
		s.measureStep(ctx, nil)
		msg, needAdmin, err := s.startStep()
		s.jobs.finish(err, msg, needAdmin)
	}()
	writeJSON(w, map[string]bool{"started": true})
}

// startStep connects and waits (bounded) until it is up.
func (s *Server) startStep() (msg string, needAdmin bool, err error) {
	s.jobs.step("start", "Bật kết nối", "running", "")
	s.stopRunner()
	if err := s.startRunner(); err != nil {
		if errors.Is(err, errNeedAdmin) {
			s.jobs.step("start", "", "skipped", "Chế độ VPS cần quyền Administrator.")
			return "Đã thiết lập xong. Bấm “Bật kết nối” và chọn Yes để cấp quyền Admin.", true, nil
		}
		s.jobs.step("start", "", "failed", err.Error())
		return "", false, err
	}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		phase, lastErr := s.phase, s.lastErr
		s.mu.Unlock()
		if phase == phaseRunning {
			s.jobs.step("start", "", "done", "")
			return "Xong! Các nguồn mạng đang được gộp.", false, nil
		}
		if phase == phaseStopped && lastErr != "" {
			s.jobs.step("start", "", "failed", lastErr)
			return "", false, errors.New(lastErr)
		}
		time.Sleep(200 * time.Millisecond)
	}
	s.jobs.step("start", "", "failed", "Quá thời gian chờ.")
	return "", false, errors.New("bật kết nối quá lâu")
}

// handleSetupVPS installs mi-server over SSH, then measures and connects.
func (s *Server) handleSetupVPS(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Host     string `json:"host"`
		SSHPort  int    `json:"sshPort"`
		User     string `json:"user"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 8<<10)).Decode(&body); err != nil || body.Host == "" || body.Password == "" {
		writeErr(w, http.StatusBadRequest, "Nhập địa chỉ IP và mật khẩu của VPS.")
		return
	}
	if !s.jobs.begin("vps", false) {
		s.busy(w)
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(s.baseCtx, 10*time.Minute)
		defer cancel()
		req := provision.Request{Host: body.Host, SSHPort: body.SSHPort, User: body.User, Password: body.Password,
			ServerBinary: serverBinary}
		if cfg, _ := s.loadAny(); cfg != nil && cfg.VPS != nil && cfg.VPS.Host == body.Host {
			req.KnownHostKey = cfg.VPS.HostKey // same VPS as before: refuse a changed host key
		}
		res, err := provision.Run(ctx, req, func(st provision.Step) { s.jobs.step(st.ID, st.Title, st.Status, st.Detail) })
		body.Password = "" // best effort: drop the password as soon as it is not needed
		if err != nil {
			s.jobs.finish(err, "", false)
			return
		}
		user, port := body.User, body.SSHPort
		if err := s.applyServer(res.ServerAddr, res.Key, &config.VPS{Host: body.Host, SSHPort: port, User: user, HostKey: res.HostKeyFingerprint}); err != nil {
			s.jobs.finish(err, "", false)
			return
		}
		s.afterServerSet(ctx)
	}()
	writeJSON(w, map[string]bool{"started": true})
}

// handleSetupCode accepts the "mi1:..." connection code printed by server-setup.sh.
func (s *Server) handleSetupCode(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "Yêu cầu không hợp lệ.")
		return
	}
	addr, key, err := provision.ParseConnectionCode(body.Code)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "Mã kết nối không đúng: hãy dán nguyên dòng bắt đầu bằng “mi1:”.")
		return
	}
	if !s.jobs.begin("code", false) {
		s.busy(w)
		return
	}
	go func() {
		s.jobs.step("code", "Đọc mã kết nối", "done", addr)
		if err := s.applyServer(addr, key, nil); err != nil {
			s.jobs.finish(err, "", false)
			return
		}
		s.afterServerSet(s.baseCtx)
	}()
	writeJSON(w, map[string]bool{"started": true})
}

// applyServer stores the VPS and switches to VPS mode.
func (s *Server) applyServer(addr, key string, vps *config.VPS) error {
	return s.updateConfig(func(c *config.Client) error {
		c.Server, c.Key, c.Connection = addr, key, config.ConnVPS
		if vps != nil {
			c.VPS = vps
		}
		return nil
	})
}

func (s *Server) afterServerSet(ctx context.Context) {
	if err := s.detectStep(ctx); err != nil {
		s.jobs.finish(err, "", false)
		return
	}
	s.stopRunner() // the old (local) connection would compete with the measurement
	s.measureStep(ctx, nil)
	msg, needAdmin, err := s.startStep()
	s.jobs.finish(err, msg, needAdmin)
}

// serverBinary finds the linux mi-server build next to mi-client.exe.
func serverBinary(arch string) (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	dir := filepath.Dir(exe)
	names := []string{"mi-server-linux-" + arch}
	if arch == "amd64" {
		names = append(names, "mi-server")
	}
	for _, n := range names {
		p := filepath.Join(dir, n)
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("thiếu file mi-server-linux-%s cạnh mi-client.exe (chạy scripts\\build.ps1)", arch)
}

// ---- preferences ----

type prefsView struct {
	Connection       string `json:"connection"`
	ServerSet        bool   `json:"serverSet"`
	AutoConnect      bool   `json:"autoConnect"`
	AutoAddDevices   bool   `json:"autoAddDevices"`
	Sticky           bool   `json:"sticky"`
	StartWithWindows bool   `json:"startWithWindows"`
	VPSHost          string `json:"vpsHost"`
}

func (s *Server) prefs() prefsView {
	v := prefsView{Connection: config.ConnLocal, AutoAddDevices: true, StartWithWindows: startWithWindows()}
	if c, _ := s.loadAny(); c != nil {
		v.Connection, v.ServerSet, v.AutoConnect, v.AutoAddDevices, v.Sticky = c.Connection, c.HasServer(), c.AutoConnect, c.AutoAdd(), c.Sticky
		if c.VPS != nil {
			v.VPSHost = c.VPS.Host
		}
	}
	return v
}

func (s *Server) handleGetPrefs(w http.ResponseWriter, r *http.Request) { writeJSON(w, s.prefs()) }

func (s *Server) handlePutPrefs(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Connection       *string `json:"connection"`
		AutoConnect      *bool   `json:"autoConnect"`
		AutoAddDevices   *bool   `json:"autoAddDevices"`
		Sticky           *bool   `json:"sticky"`
		StartWithWindows *bool   `json:"startWithWindows"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<10)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "Yêu cầu không hợp lệ.")
		return
	}
	if body.StartWithWindows != nil {
		if err := setStartWithWindows(*body.StartWithWindows, s.opts.ConfigPath); err != nil {
			writeErr(w, http.StatusInternalServerError, "Không đặt được khởi động cùng Windows: "+err.Error())
			return
		}
	}
	modeChanged := false
	if body.Connection != nil || body.AutoConnect != nil || body.AutoAddDevices != nil || body.Sticky != nil {
		err := s.updateConfig(func(c *config.Client) error {
			if body.Connection != nil && *body.Connection != c.Connection {
				if *body.Connection == config.ConnVPS && !c.HasServer() {
					return errors.New("Chưa có VPS: hãy cài VPS hoặc dán mã kết nối trước.")
				}
				if *body.Connection != config.ConnVPS && *body.Connection != config.ConnLocal {
					return errors.New("chế độ không hợp lệ")
				}
				c.Connection, modeChanged = *body.Connection, true
			}
			if body.AutoConnect != nil {
				c.AutoConnect = *body.AutoConnect
			}
			if body.AutoAddDevices != nil {
				v := *body.AutoAddDevices
				c.AutoAddDevices = &v
			}
			if body.Sticky != nil {
				c.Sticky = *body.Sticky
			}
			return nil
		})
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	resp := map[string]any{"prefs": s.prefs(), "restartNeeded": modeChanged && s.running() != nil}
	writeJSON(w, resp)
}

// ---- automation: connect on launch, add phones when plugged in ----

func (s *Server) autoLoop(ctx context.Context) {
	// Give device detection a moment before acting on it.
	select {
	case <-ctx.Done():
		return
	case <-time.After(2 * time.Second):
	}
	if cfg, err := config.LoadClient(s.opts.ConfigPath); err == nil && (s.opts.AutoStart || cfg.AutoConnect) {
		if cfg.Connection != config.ConnVPS || runtime.GOOS != "windows" || isElevated() {
			if err := s.startRunner(); err != nil {
				log.Printf("tự bật kết nối: %v", err)
			}
		} else {
			log.Printf("tự bật kết nối: chế độ VPS cần quyền Admin, hãy bấm “Bật kết nối”")
		}
	}
	t := time.NewTicker(3 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.autoAdd()
			s.measureUnmeasured()
		}
	}
}

// autoAdd adds phones (and a main line, if missing) that appear while the app runs.
func (s *Server) autoAdd() {
	if s.jobs.running() {
		return
	}
	cfg, err := config.LoadClient(s.opts.ConfigPath)
	if err != nil || !cfg.AutoAdd() {
		return
	}
	have := map[string]bool{}
	hasMain, exts := false, 0
	for _, p := range cfg.Paths {
		have[p.Interface] = true
		if p.Role == config.RoleMain {
			hasMain = true
		} else {
			exts++
		}
	}
	for _, d := range s.devices.Snapshot() {
		if !d.Usable || have[d.Interface] || cfg.IsIgnored(d.Interface) {
			continue
		}
		var role string
		switch {
		case d.Role == devinfo.RoleExtension && exts < config.MaxExtensions:
			role = config.RoleExtension
		case d.Role == devinfo.RoleMain && !hasMain:
			role = config.RoleMain
		default:
			continue
		}
		p := config.Path{Name: d.Name, Interface: d.Interface, Role: role}
		if role == config.RoleExtension {
			p.Device = d.Name
		}
		if _, _, err := s.addPath(p); err != nil {
			log.Printf("tự thêm %s: %v", d.Name, err)
		}
		return // one at a time; the next tick sees the updated config
	}
}
