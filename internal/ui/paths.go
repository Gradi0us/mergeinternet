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
	"strconv"
	"strings"
	"time"

	"mergeinternet/internal/client"
	"mergeinternet/internal/config"
	"mergeinternet/internal/devinfo"
)

// ---- status ----

type linkView struct {
	ID         int     `json:"id"`
	Name       string  `json:"name"`   // label chosen in settings
	Device     string  `json:"device"` // who provides the link, e.g. "vivo X300 Pro"
	Iface      string  `json:"iface"`
	Kind       string  `json:"kind"`
	Via        string  `json:"via"`
	Role       string  `json:"role"`
	Present    bool    `json:"present"` // the interface is plugged in / connected right now
	Alive      bool    `json:"alive"`
	Enabled    bool    `json:"enabled"`
	Capped     bool    `json:"capped"`
	Measured   bool    `json:"measured"` // weights come from a speed test
	RTTms      float64 `json:"rttMs"`
	RxMbps     float64 `json:"rxMbps"`
	TxMbps     float64 `json:"txMbps"`
	RxBytes    uint64  `json:"rxBytes"`
	TxBytes    uint64  `json:"txBytes"`
	UpMbps     float64 `json:"upMbps"`
	DnMbps     float64 `json:"downMbps"`
	LimitMbps  float64 `json:"limitMbps"`
	QuotaGB    float64 `json:"quotaGB"`
	UsageBytes uint64  `json:"usageBytes"`
	UsageSince int64   `json:"usageSince"`
	// SameNetAs names the main line when this LAN / Wi-Fi line reached the
	// internet with the same public IP at the last measurement (same router).
	SameNetAs string `json:"sameNetAs,omitempty"`
}

type statusView struct {
	Phase         string           `json:"phase"`
	Error         string           `json:"error"`
	Elevated      bool             `json:"elevated"`
	Wintun        bool             `json:"wintun"`
	Configured    bool             `json:"configured"` // a runnable config exists
	ServerSet     bool             `json:"serverSet"`  // a VPS address + key are known
	Connection    string           `json:"connection"` // local | vps
	ConfigPath    string           `json:"configPath"`
	StartedAt     int64            `json:"startedAt"`
	Server        string           `json:"server"`
	Mode          string           `json:"mode"`
	MaxExtensions int              `json:"maxExtensions"`
	Links         []linkView       `json:"links"`
	Candidates    []devinfo.Device `json:"candidates"` // detected devices not configured yet
	Reorder       struct {
		Delivered uint64 `json:"delivered"`
		Lost      uint64 `json:"lost"`
		Late      uint64 `json:"late"`
	} `json:"reorder"`
	Dropped uint64   `json:"dropped"`
	History []Sample `json:"history"`
	Job     *jobView `json:"job"`
}

// loadAny returns the runnable config, or the VPS-only draft, or nil.
func (s *Server) loadAny() (cfg *config.Client, runnable bool) {
	if c, err := config.LoadClient(s.opts.ConfigPath); err == nil {
		return c, true
	}
	if c, err := config.LoadDraft(s.opts.ConfigPath); err == nil {
		return c, false
	}
	return nil, false
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	v := statusView{Elevated: isElevated(), Wintun: wintunPresent(), ConfigPath: s.opts.ConfigPath,
		MaxExtensions: config.MaxExtensions, Links: []linkView{}, Candidates: []devinfo.Device{}, History: []Sample{},
		Connection: config.ConnLocal, Job: s.jobs.view()}
	cfg, runnable := s.loadAny()
	v.Configured = runnable
	if cfg != nil {
		v.ServerSet = cfg.HasServer()
		v.Connection = cfg.Connection
		if v.ServerSet {
			v.Server = cfg.Server
		}
		v.Mode = cfg.SchedMode.String()
	}

	detected := map[string]devinfo.Device{}
	for _, d := range s.devices.Snapshot() {
		detected[d.Interface] = d
	}

	s.mu.Lock()
	v.Phase, v.Error = s.phase, s.lastErr
	run := s.runner
	if run != nil {
		v.History = s.smp.hist
	}
	rates := s.smp.rates
	s.mu.Unlock()

	used := map[string]bool{}
	if run != nil {
		v.Connection = run.Mode()
		v.StartedAt = run.StartedAt().UnixMilli()
		if t, ok := run.(*client.Tunnel); ok {
			_, rs, _ := t.Engine().Stats()
			v.Reorder.Delivered, v.Reorder.Lost, v.Reorder.Late = rs.Delivered, rs.Skipped, rs.Late
			v.Dropped = t.Engine().Dropped()
		}
		for _, pi := range run.Paths() {
			lv := s.linkFromPath(int(pi.ID), pi.Path, detected)
			lv.Enabled, lv.Capped, lv.Alive = pi.Enabled, pi.Capped, pi.Alive
			lv.RTTms = float64(pi.RTT.Microseconds()) / 1000
			lv.RxBytes, lv.TxBytes = pi.RxBytes, pi.TxBytes
			lv.UsageBytes, lv.UsageSince = pi.UsageBytes, pi.UsageSince.UnixMilli()
			rate := rates[pi.ID]
			lv.RxMbps, lv.TxMbps = round2(rate.rx), round2(rate.tx)
			used[pi.Path.Interface] = true
			v.Links = append(v.Links, lv)
		}
	} else if cfg != nil {
		for i, p := range cfg.Paths {
			lv := s.linkFromPath(i, p, detected)
			lv.Enabled = true
			bytes, since := s.usage.Get(p.Interface)
			lv.UsageBytes, lv.UsageSince = bytes, since.UnixMilli()
			if q := p.QuotaBytes(); q > 0 && bytes >= q {
				lv.Capped, lv.Enabled = true, false
			}
			used[p.Interface] = true
			v.Links = append(v.Links, lv)
		}
	}
	markSharedUplinks(v.Links, cfg)
	for _, d := range s.devices.Snapshot() {
		if d.Usable && d.Role != devinfo.RoleIgnore && !used[d.Interface] && (cfg == nil || !cfg.IsIgnored(d.Interface)) {
			v.Candidates = append(v.Candidates, d)
		}
	}
	writeJSON(w, v)
}

// markSharedUplinks flags LAN / Wi-Fi lines whose last measured public IP is
// the main line's: they share its router, so they add no speed.
func markSharedUplinks(links []linkView, cfg *config.Client) {
	if cfg == nil {
		return
	}
	ipOf := map[string]string{}
	mainIP := ""
	for _, p := range cfg.Paths {
		ipOf[p.Interface] = p.PublicIP
		if p.Role == config.RoleMain {
			mainIP = p.PublicIP
		}
	}
	mainName := ""
	for _, l := range links {
		if l.Role == config.RoleMain {
			mainName = l.Device
		}
	}
	if mainIP == "" {
		return
	}
	for i, l := range links {
		phone := l.Via == string(devinfo.ViaUSB) || l.Via == string(devinfo.ViaHotspot)
		if l.Role != config.RoleMain && !phone && ipOf[l.Iface] == mainIP {
			links[i].SameNetAs = mainName
		}
	}
}

func (s *Server) linkFromPath(id int, p config.Path, detected map[string]devinfo.Device) linkView {
	lv := linkView{ID: id, Name: p.Name, Device: p.Device, Iface: p.Interface, Role: p.Role,
		UpMbps: p.UpMbps, DnMbps: p.DownMbps, LimitMbps: p.LimitMbps, QuotaGB: p.QuotaGB, Measured: p.MeasuredAt != ""}
	if d, ok := detected[p.Interface]; ok {
		lv.Kind, lv.Via, lv.Present = d.Kind, string(d.Via), d.Usable
		if d.Name != "" {
			lv.Device = d.Name // live name wins: the phone on this port may have changed
		}
	}
	if lv.Device == "" {
		lv.Device = p.Name
	}
	if lv.Via == "" && p.Role == config.RoleMain {
		lv.Via = string(devinfo.ViaLAN)
	}
	return lv
}

// ---- devices ----

func (s *Server) handleDevices(w http.ResponseWriter, r *http.Request) {
	out := s.devices.Snapshot()
	if out == nil {
		out = []devinfo.Device{}
	}
	writeJSON(w, out)
}

func (s *Server) handleDevicesRefresh(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	out := s.devices.Refresh(ctx)
	if out == nil {
		out = []devinfo.Device{}
	}
	writeJSON(w, out)
}

// ---- config ----

func (s *Server) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	b, err := os.ReadFile(s.opts.ConfigPath)
	if errors.Is(err, os.ErrNotExist) {
		writeJSON(w, map[string]any{"exists": false, "config": nil, "path": s.opts.ConfigPath})
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	var raw json.RawMessage
	if json.Unmarshal(b, &raw) != nil {
		writeJSON(w, map[string]any{"exists": true, "config": nil, "path": s.opts.ConfigPath, "error": "File cấu hình bị hỏng (JSON sai)."})
		return
	}
	writeJSON(w, map[string]any{"exists": true, "config": raw, "path": s.opts.ConfigPath})
}

func (s *Server) handlePutConfig(w http.ResponseWriter, r *http.Request) {
	b, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	cfg, err := config.ParseClient(b)
	if err != nil {
		writeErr(w, http.StatusBadRequest, friendlyConfigErr(err))
		return
	}
	s.cfgMu.Lock()
	err = config.SaveClient(s.opts.ConfigPath, cfg)
	s.cfgMu.Unlock()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	log.Printf("đã lưu cấu hình vào %s", s.opts.ConfigPath)
	writeJSON(w, map[string]bool{"saved": true, "restartNeeded": s.running() != nil})
}

// updateConfig applies f to the saved config (or draft) and writes it back.
// A missing file starts from an empty local-mode config.
func (s *Server) updateConfig(f func(c *config.Client) error) error {
	s.cfgMu.Lock()
	defer s.cfgMu.Unlock()
	cfg, err := config.LoadClient(s.opts.ConfigPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			cfg = &config.Client{Connection: config.ConnLocal, Mode: "aggregate"}
		} else if cfg, err = config.LoadDraft(s.opts.ConfigPath); err != nil {
			return err
		}
	}
	if err := f(cfg); err != nil {
		return err
	}
	return config.SaveClient(s.opts.ConfigPath, cfg)
}

// friendlyConfigErr translates the validation errors a user can actually cause from the UI.
func friendlyConfigErr(err error) string {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "extension devices"):
		return fmt.Sprintf("Chỉ hỗ trợ tối đa %d thiết bị mở rộng. Bỏ bớt một thiết bị trước.", config.MaxExtensions)
	case strings.Contains(msg, "main connection"):
		return "Chỉ được chọn 1 đường chính cho PC (LAN hoặc Wi-Fi)."
	case strings.Contains(msg, "used twice"):
		return "Card mạng này đã có trong danh sách."
	case strings.Contains(msg, "up_mbps and down_mbps"):
		return "Cần điền tốc độ tải xuống và tải lên (lớn hơn 0) cho mỗi đường."
	case strings.Contains(msg, "server:"):
		return "Địa chỉ máy chủ phải có dạng IP:cổng, ví dụ 203.0.113.10:51820."
	case strings.Contains(msg, "key:"):
		return "Key không hợp lệ: hãy dán đúng chuỗi in ra bởi server-setup.sh."
	case errors.Is(err, config.ErrNoPaths):
		return "Chưa chọn đường mạng nào."
	}
	return msg
}

// ---- per-link controls ----

// pathRef resolves a link ID to its config path (from the running connection if any).
func (s *Server) pathRef(id int) (config.Path, client.Runner, error) {
	if r := s.running(); r != nil {
		for _, pi := range r.Paths() {
			if int(pi.ID) == id {
				return pi.Path, r, nil
			}
		}
		return config.Path{}, r, client.ErrNoPath
	}
	cfg, err := config.LoadClient(s.opts.ConfigPath)
	if err != nil {
		return config.Path{}, nil, err
	}
	if id < 0 || id >= len(cfg.Paths) {
		return config.Path{}, nil, client.ErrNoPath
	}
	return cfg.Paths[id], nil, nil
}

func (s *Server) handleLink(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.PathValue("id"))
	var body struct {
		Enabled   *bool    `json:"enabled"`
		LimitMbps *float64 `json:"limitMbps"`
		QuotaGB   *float64 `json:"quotaGB"`
	}
	if err != nil || json.NewDecoder(io.LimitReader(r.Body, 1<<10)).Decode(&body) != nil {
		writeErr(w, http.StatusBadRequest, "Yêu cầu không hợp lệ.")
		return
	}
	p, run, err := s.pathRef(id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "Không tìm thấy đường mạng này.")
		return
	}

	if body.LimitMbps != nil || body.QuotaGB != nil {
		limit, quota := p.LimitMbps, p.QuotaGB
		if body.LimitMbps != nil {
			limit = *body.LimitMbps
		}
		if body.QuotaGB != nil {
			quota = *body.QuotaGB
		}
		if limit < 0 || quota < 0 || limit > 100000 || quota > 100000 {
			writeErr(w, http.StatusBadRequest, "Giá trị giới hạn không hợp lệ.")
			return
		}
		if err := s.updateConfig(func(c *config.Client) error {
			for i := range c.Paths {
				if c.Paths[i].Interface == p.Interface {
					c.Paths[i].LimitMbps, c.Paths[i].QuotaGB = limit, quota
				}
			}
			return nil
		}); err != nil {
			writeErr(w, http.StatusInternalServerError, "Không lưu được cấu hình: "+err.Error())
			return
		}
		if run != nil {
			if err := run.SetLimits(uint8(id), limit, quota); err != nil {
				writeErr(w, http.StatusConflict, err.Error())
				return
			}
		}
		log.Printf("path %s: giới hạn %.1f Mbps, hạn mức %.2f GB", p.Name, limit, quota)
	}

	if body.Enabled != nil {
		if run == nil {
			writeErr(w, http.StatusConflict, "Kết nối chưa chạy.")
			return
		}
		if err := run.SetEnabled(uint8(id), *body.Enabled); err != nil {
			writeErr(w, http.StatusConflict, err.Error())
			return
		}
		state := "bật"
		if !*body.Enabled {
			state = "ngắt"
		}
		log.Printf("path %s: %s bởi người dùng", p.Name, state)
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) handleResetUsage(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "Yêu cầu không hợp lệ.")
		return
	}
	p, run, err := s.pathRef(id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "Không tìm thấy đường mạng này.")
		return
	}
	if run != nil {
		err = run.ResetUsage(uint8(id))
	} else {
		s.usage.Reset(p.Interface)
		err = s.usage.Save()
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	log.Printf("path %s: đặt lại số liệu dung lượng", p.Name)
	writeJSON(w, map[string]bool{"ok": true})
}

// handleAddPath adds a detected device to the config and, if a connection
// runs, starts using it immediately.
func (s *Server) handleAddPath(w http.ResponseWriter, r *http.Request) {
	var p config.Path
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&p); err != nil || p.Interface == "" {
		writeErr(w, http.StatusBadRequest, "Yêu cầu không hợp lệ.")
		return
	}
	live, id, err := s.addPath(p)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	resp := map[string]any{"saved": true, "live": live}
	if live {
		resp["id"] = id
		go s.measureUnmeasured()
	}
	writeJSON(w, resp)
}

// addPath saves a new path (role limits enforced) and hot-adds it to the
// running connection. Placeholder speeds are filled in when missing.
func (s *Server) addPath(p config.Path) (live bool, id uint8, err error) {
	if p.Name == "" {
		p.Name = p.Device
	}
	if p.Name == "" {
		p.Name = p.Interface
	}
	if p.Role == "" {
		p.Role = config.RoleExtension
	}
	if p.DownMbps <= 0 || p.UpMbps <= 0 {
		p.DownMbps, p.UpMbps = config.DefaultSpeeds(p.Role)
	}
	if err := s.updateConfig(func(c *config.Client) error {
		c.Paths = append(c.Paths, p)
		c.Ignored = without(c.Ignored, p.Interface)
		_, err := config.ParseClient(mustJSON(c)) // validate role limits etc. before saving
		if errors.Is(err, config.ErrNoPaths) {
			err = nil
		}
		return err
	}); err != nil {
		return false, 0, errors.New(friendlyConfigErr(err))
	}
	log.Printf("đã thêm %q (%s, card %q)", p.Name, p.Role, p.Interface)
	if run := s.running(); run != nil {
		id, err := s.hotAdd(run, p)
		if err != nil {
			return false, 0, err
		}
		return true, id, nil
	}
	return false, 0, nil
}

func without(list []string, v string) []string {
	out := list[:0:0]
	for _, x := range list {
		if x != v {
			out = append(out, x)
		}
	}
	return out
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}
