// Package sysproxy points the Windows per-user system proxy (WinINet, used by
// Edge/Chrome and most desktop apps) at the local load-balancing proxy, and
// puts the previous settings back afterwards - also after a crash, from a
// backup file written before anything is changed.
package sysproxy

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// Settings is the part of the WinINet proxy configuration we touch.
type Settings struct {
	Flags         uint32 `json:"flags"` // PROXY_TYPE_* bits
	ProxyServer   string `json:"proxy_server"`
	Bypass        string `json:"bypass"`
	AutoConfigURL string `json:"auto_config_url"`
}

// Bypass keeps local and private-network destinations off the proxy.
const Bypass = "<local>;localhost;127.*;10.*;172.16.*;172.17.*;172.18.*;172.19.*;172.2*;172.30.*;172.31.*;192.168.*;169.254.*"

type backup struct {
	Previous Settings `json:"previous"`
	Ours     string   `json:"ours"` // the proxy server we installed
}

// Manager installs and restores the proxy. BackupPath persists the previous
// settings so Recover can undo a session that ended without Restore.
type Manager struct {
	BackupPath string
	get        func() (Settings, error)
	set        func(Settings) error
}

// New returns a Manager for the current user's WinINet settings.
func New(backupPath string) *Manager {
	return &Manager{BackupPath: backupPath, get: platformGet, set: platformSet}
}

// DefaultBackupPath is %LOCALAPPDATA%\mergeinternet\proxy-backup.json.
func DefaultBackupPath() string {
	dir, err := os.UserCacheDir() // %LOCALAPPDATA% on Windows
	if err != nil {
		dir = os.TempDir()
	}
	return filepath.Join(dir, "mergeinternet", "proxy-backup.json")
}

var ErrUnsupported = errors.New("đặt proxy hệ thống chỉ hỗ trợ trên Windows")

// Current reads the system proxy settings without changing anything.
func Current() (Settings, error) { return platformGet() }

// IsOurs reports whether the system proxy currently points at server.
func IsOurs(server string) bool {
	s, err := platformGet()
	return err == nil && s.Flags&flagProxy != 0 && s.ProxyServer == server
}

// Install saves the current settings to the backup file, then makes server
// (e.g. "127.0.0.1:7879") the system proxy.
func (m *Manager) Install(server string) error {
	cur, err := m.get()
	if err != nil {
		return err
	}
	// If a previous session crashed, our proxy may still be set: keep the
	// older backup (the real user settings) instead of saving our own.
	if prev, ok := m.readBackup(); ok && cur.ProxyServer == prev.Ours {
		cur = prev.Previous
	}
	if err := m.writeBackup(backup{Previous: cur, Ours: server}); err != nil {
		return err
	}
	return m.set(Settings{Flags: flagDirect | flagProxy, ProxyServer: server, Bypass: Bypass})
}

// Restore puts the saved settings back and deletes the backup.
func (m *Manager) Restore() error {
	b, ok := m.readBackup()
	if !ok {
		return nil
	}
	if err := m.set(b.Previous); err != nil {
		return err
	}
	return os.Remove(m.BackupPath)
}

// Recover undoes a session that ended without Restore (crash, power loss):
// if our proxy is still installed, the saved settings are put back. If the
// user has changed the proxy since, their change is left alone.
func (m *Manager) Recover() (bool, error) {
	b, ok := m.readBackup()
	if !ok {
		return false, nil
	}
	cur, err := m.get()
	if err != nil {
		return false, err
	}
	if cur.ProxyServer != b.Ours || cur.Flags&flagProxy == 0 {
		return false, os.Remove(m.BackupPath)
	}
	if err := m.set(b.Previous); err != nil {
		return false, err
	}
	return true, os.Remove(m.BackupPath)
}

func (m *Manager) readBackup() (backup, bool) {
	var b backup
	data, err := os.ReadFile(m.BackupPath)
	if err != nil || json.Unmarshal(data, &b) != nil || b.Ours == "" {
		return backup{}, false
	}
	return b, true
}

func (m *Manager) writeBackup(b backup) error {
	if err := os.MkdirAll(filepath.Dir(m.BackupPath), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return err
	}
	tmp := m.BackupPath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, m.BackupPath)
}

// WinINet PROXY_TYPE_* flags (wininet.h).
const (
	flagDirect     = 0x1
	flagProxy      = 0x2
	flagAutoURL    = 0x4
	flagAutoDetect = 0x8
)
