// Package config loads and validates the JSON config files of mi-client and mi-server.
package config

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"time"

	"mergeinternet/internal/proto"
	"mergeinternet/internal/sched"
)

// DefaultMTU leaves room for the 44-byte tunnel overhead (proto.DataOverhead)
// plus 28 bytes of outer IPv4/UDP headers on mobile links whose MTU is often below 1500.
const DefaultMTU = 1360

// Path roles: the PC's own connection versus a phone lending its internet.
const (
	RoleMain      = "main"
	RoleExtension = "extension"

	MaxMain       = 1
	MaxExtensions = 5
)

type Path struct {
	Name      string  `json:"name"`
	Interface string  `json:"interface"`
	UpMbps    float64 `json:"up_mbps"`
	DownMbps  float64 `json:"down_mbps"`
	// Role is "main" or "extension"; empty in older configs (see ParseClient).
	Role string `json:"role,omitempty"`
	// Device is the detected name of what provides this link, e.g. "vivo X300 Pro".
	Device string `json:"device,omitempty"`
	// LimitMbps caps the link in each direction; 0 = no cap.
	LimitMbps float64 `json:"limit_mbps,omitempty"`
	// QuotaGB switches the link off once this much data has crossed it; 0 = no quota.
	QuotaGB float64 `json:"quota_gb,omitempty"`
	// MeasuredAt is set when Up/DownMbps come from a speed test rather than defaults.
	MeasuredAt string `json:"measured_at,omitempty"`
	// PublicIP is the address this line reached the internet with at the last
	// measurement. A LAN / Wi-Fi line with the same public IP as the main line
	// shares its uplink (same router), so it adds no speed.
	PublicIP string `json:"public_ip,omitempty"`
}

func (p Path) LimitKbps() uint32 { return uint32(p.LimitMbps * 1000) }

// Weights used before a link has been measured; MeasurePaths replaces them.
const (
	DefaultMainDownMbps = 100
	DefaultMainUpMbps   = 30
	DefaultExtDownMbps  = 30
	DefaultExtUpMbps    = 8
)

// DefaultSpeeds returns placeholder weights for a role.
func DefaultSpeeds(role string) (down, up float64) {
	if role == RoleMain {
		return DefaultMainDownMbps, DefaultMainUpMbps
	}
	return DefaultExtDownMbps, DefaultExtUpMbps
}

// AutoAdd reports whether newly plugged-in phones are added automatically.
func (c *Client) AutoAdd() bool { return c.AutoAddDevices == nil || *c.AutoAddDevices }

// QuotaBytes returns the quota in bytes (0 = none).
func (p Path) QuotaBytes() uint64 { return uint64(p.QuotaGB * 1e9) }

type ClientTun struct {
	Name    string   `json:"name"`
	Address string   `json:"address"`
	Gateway string   `json:"gateway"`
	MTU     int      `json:"mtu"`
	DNS     []string `json:"dns"`
}

// VPS remembers how the server was installed, so the app can re-install or
// upgrade it later. The SSH password is never stored.
type VPS struct {
	Host    string `json:"host"`
	SSHPort int    `json:"ssh_port,omitempty"`
	User    string `json:"user,omitempty"`
	HostKey string `json:"host_key,omitempty"` // SHA256 fingerprint pinned on first install
}

// Connection modes.
const (
	// ConnLocal needs no server: each new connection goes out one of the links
	// through a local proxy. Multi-connection traffic gets faster; a single
	// connection does not.
	ConnLocal = "local"
	// ConnVPS bonds every packet through mi-server, so even one connection gets faster.
	ConnVPS = "vps"
)

type Client struct {
	// Connection is ConnLocal or ConnVPS; empty = vps when a server is set, else local.
	Connection string    `json:"connection,omitempty"`
	Server     string    `json:"server,omitempty"`
	Key        string    `json:"key,omitempty"`
	Mode       string    `json:"mode"`
	ReorderMS  *int      `json:"reorder_ms"`
	Tun        ClientTun `json:"tun"`
	Paths      []Path    `json:"paths"`

	// AutoConnect starts the tunnel as soon as the app opens.
	AutoConnect bool `json:"auto_connect,omitempty"`
	// Sticky (local mode): one website = one path, friendlier to logins, less speed per site.
	Sticky bool `json:"sticky,omitempty"`
	// AutoAddDevices adds a phone automatically when it is plugged in (default on).
	AutoAddDevices *bool `json:"auto_add_devices,omitempty"`
	// Ignored lists interfaces the user chose not to use; they are never auto-added.
	Ignored []string `json:"ignored,omitempty"`
	VPS     *VPS     `json:"vps,omitempty"`

	// Parsed values.
	ServerAddr netip.AddrPort `json:"-"`
	KeyBytes   []byte         `json:"-"`
	SchedMode  sched.Mode     `json:"-"`
	Reorder    time.Duration  `json:"-"`
	TunPrefix  netip.Prefix   `json:"-"`
	TunGateway netip.Addr     `json:"-"`
	DNSAddrs   []netip.Addr   `json:"-"`
}

type ServerTun struct {
	Name    string `json:"name"`
	Address string `json:"address"`
	MTU     int    `json:"mtu"`
}

type Server struct {
	Listen    string    `json:"listen"`
	Key       string    `json:"key"`
	Mode      string    `json:"mode"`
	ReorderMS *int      `json:"reorder_ms"`
	Tun       ServerTun `json:"tun"`

	KeyBytes  []byte        `json:"-"`
	SchedMode sched.Mode    `json:"-"`
	Reorder   time.Duration `json:"-"`
	TunPrefix netip.Prefix  `json:"-"`
}

const defaultReorderMS = 60

// ErrNoPaths: the server part is set but no network path has been chosen yet.
var ErrNoPaths = errors.New("paths: at least one path is required")

func LoadClient(path string) (*Client, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	c, err := ParseClient(b)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

// SaveClient validates c and writes it to path (key included, so owner-only on Unix).
// A config without paths is accepted as a draft: the VPS part of setup can
// finish before any network path is chosen.
func SaveClient(path string, c *Client) error {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	if _, err := ParseClient(b); err != nil && !errors.Is(err, ErrNoPaths) {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// LoadDraft reads a config that may not have paths yet (see SaveClient).
// The server address and key are still validated.
func LoadDraft(path string) (*Client, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	c, err := ParseClient(b)
	if err == nil {
		return c, nil
	}
	if !errors.Is(err, ErrNoPaths) {
		return nil, err
	}
	var draft Client
	if err := json.Unmarshal(b, &draft); err != nil {
		return nil, err
	}
	draft.Paths = nil
	draft.Paths = append(draft.Paths, Path{Name: "_", Interface: "_", UpMbps: 1, DownMbps: 1})
	full, err := ParseClient(mustMarshal(&draft)) // fills defaults and parsed fields
	if err != nil {
		return nil, err
	}
	full.Paths = nil
	return full, nil
}

func mustMarshal(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

// HasServer reports whether a VPS address and key are configured.
func (c *Client) HasServer() bool { return c.ServerAddr.IsValid() && len(c.KeyBytes) == KeyLen }

// KeyLen is the length of the shared tunnel key.
const KeyLen = 32

// IsIgnored reports whether the user opted out of an interface.
func (c *Client) IsIgnored(iface string) bool {
	for _, i := range c.Ignored {
		if i == iface {
			return true
		}
	}
	return false
}

// ParseClient decodes and validates a client config, filling defaults.
func ParseClient(b []byte) (*Client, error) {
	var c Client
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, err
	}
	var err error
	switch c.Connection {
	case "":
		c.Connection = ConnLocal
		if c.Server != "" {
			c.Connection = ConnVPS
		}
	case ConnLocal, ConnVPS:
	default:
		return nil, fmt.Errorf("connection: want %q or %q, got %q", ConnLocal, ConnVPS, c.Connection)
	}
	// The server is optional in local mode, but if present it must be valid
	// (the user may switch to VPS mode later).
	if c.Connection == ConnVPS || c.Server != "" || c.Key != "" {
		if c.ServerAddr, err = netip.ParseAddrPort(c.Server); err != nil || !c.ServerAddr.Addr().Is4() {
			return nil, fmt.Errorf("server: want IPv4 ip:port, got %q", c.Server)
		}
		if c.KeyBytes, err = DecodeKey(c.Key); err != nil {
			return nil, err
		}
	}
	if c.SchedMode, err = sched.ParseMode(c.Mode); err != nil {
		return nil, err
	}
	c.Reorder = reorderDelay(c.ReorderMS)
	if c.Tun.Name == "" {
		c.Tun.Name = "mi0"
	}
	if c.Tun.Address == "" {
		c.Tun.Address = "10.66.0.2/24"
	}
	if c.Tun.Gateway == "" {
		c.Tun.Gateway = "10.66.0.1"
	}
	if c.Tun.MTU == 0 {
		c.Tun.MTU = DefaultMTU
	}
	if c.TunPrefix, err = netip.ParsePrefix(c.Tun.Address); err != nil {
		return nil, fmt.Errorf("tun.address: %w", err)
	}
	if c.TunGateway, err = netip.ParseAddr(c.Tun.Gateway); err != nil {
		return nil, fmt.Errorf("tun.gateway: %w", err)
	}
	for _, d := range c.Tun.DNS {
		a, err := netip.ParseAddr(d)
		if err != nil {
			return nil, fmt.Errorf("tun.dns: %w", err)
		}
		c.DNSAddrs = append(c.DNSAddrs, a)
	}
	if len(c.Paths) == 0 {
		return nil, ErrNoPaths
	}
	if len(c.Paths) > 255 {
		return nil, errors.New("paths: at most 255 paths")
	}
	if err := normalizeRoles(c.Paths); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for i, p := range c.Paths {
		if p.Name == "" {
			return nil, fmt.Errorf("paths[%d]: name is required", i)
		}
		if seen[p.Interface] {
			return nil, fmt.Errorf("paths[%d]: interface %q used twice", i, p.Interface)
		}
		seen[p.Interface] = true
		if p.UpMbps <= 0 || p.DownMbps <= 0 {
			return nil, fmt.Errorf("paths[%d] %q: up_mbps and down_mbps must be > 0 (measure each link with a speed test)", i, p.Name)
		}
		if p.LimitMbps < 0 || p.QuotaGB < 0 {
			return nil, fmt.Errorf("paths[%d] %q: limit_mbps and quota_gb must be >= 0", i, p.Name)
		}
	}
	return &c, nil
}

// normalizeRoles fills roles missing in older configs (first path = the PC's
// main line, the rest = extensions) and enforces 1 main + up to 2 extensions.
func normalizeRoles(paths []Path) error {
	hasMain := false
	for _, p := range paths {
		hasMain = hasMain || p.Role == RoleMain
	}
	var mains, exts int
	for i := range paths {
		p := &paths[i]
		switch p.Role {
		case "":
			if !hasMain {
				p.Role, hasMain = RoleMain, true
			} else {
				p.Role = RoleExtension
			}
		case RoleMain, RoleExtension:
		default:
			return fmt.Errorf("paths[%d] %q: role must be %q or %q", i, p.Name, RoleMain, RoleExtension)
		}
		if p.Role == RoleMain {
			mains++
		} else {
			exts++
		}
	}
	if mains > MaxMain {
		return fmt.Errorf("paths: only %d main connection allowed, got %d", MaxMain, mains)
	}
	if exts > MaxExtensions {
		return fmt.Errorf("paths: at most %d extension devices, got %d", MaxExtensions, exts)
	}
	return nil
}

func LoadServer(path string) (*Server, error) {
	var s Server
	if err := load(path, &s); err != nil {
		return nil, err
	}
	var err error
	if s.Listen == "" {
		s.Listen = ":51820"
	}
	if s.KeyBytes, err = DecodeKey(s.Key); err != nil {
		return nil, err
	}
	if s.SchedMode, err = sched.ParseMode(s.Mode); err != nil {
		return nil, err
	}
	s.Reorder = reorderDelay(s.ReorderMS)
	if s.Tun.Name == "" {
		s.Tun.Name = "mi0"
	}
	if s.Tun.Address == "" {
		s.Tun.Address = "10.66.0.1/24"
	}
	if s.Tun.MTU == 0 {
		s.Tun.MTU = DefaultMTU
	}
	if s.TunPrefix, err = netip.ParsePrefix(s.Tun.Address); err != nil {
		return nil, fmt.Errorf("tun.address: %w", err)
	}
	return &s, nil
}

func load(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

func reorderDelay(ms *int) time.Duration {
	if ms == nil {
		return defaultReorderMS * time.Millisecond
	}
	return time.Duration(*ms) * time.Millisecond
}

func DecodeKey(s string) ([]byte, error) {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil || len(b) != proto.KeyLen {
		return nil, errors.New("key: want 32 random bytes in base64 (generate with `genkey`)")
	}
	return b, nil
}

func GenerateKey() string {
	b := make([]byte, proto.KeyLen)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.StdEncoding.EncodeToString(b)
}
