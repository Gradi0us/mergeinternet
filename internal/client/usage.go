package client

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Usage remembers how much data crossed each interface, across restarts, so a
// phone's data quota keeps counting until the user resets it.
type Usage struct {
	mu    sync.Mutex
	path  string
	data  map[string]usageEntry
	dirty bool
}

type usageEntry struct {
	Bytes uint64    `json:"bytes"`
	Since time.Time `json:"since"`
}

// UsagePathFor keeps usage.json next to the config file.
func UsagePathFor(configPath string) string {
	return filepath.Join(filepath.Dir(configPath), "usage.json")
}

// LoadUsage reads the usage file; a missing or corrupt file starts empty.
func LoadUsage(path string) *Usage {
	u := &Usage{path: path, data: map[string]usageEntry{}}
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &u.data)
		if u.data == nil {
			u.data = map[string]usageEntry{}
		}
	}
	return u
}

// Add records n more bytes for iface and returns the new total.
func (u *Usage) Add(iface string, n uint64) uint64 {
	u.mu.Lock()
	defer u.mu.Unlock()
	e, ok := u.data[iface]
	if !ok {
		e.Since = time.Now()
	}
	if n > 0 {
		e.Bytes += n
		u.dirty = true
	}
	u.data[iface] = e
	return e.Bytes
}

func (u *Usage) Get(iface string) (uint64, time.Time) {
	u.mu.Lock()
	defer u.mu.Unlock()
	e := u.data[iface]
	return e.Bytes, e.Since
}

func (u *Usage) Reset(iface string) {
	u.mu.Lock()
	u.data[iface] = usageEntry{Since: time.Now()}
	u.dirty = true
	u.mu.Unlock()
}

// Save writes the file atomically if anything changed.
func (u *Usage) Save() error {
	u.mu.Lock()
	if !u.dirty || u.path == "" {
		u.mu.Unlock()
		return nil
	}
	b, err := json.MarshalIndent(u.data, "", "  ")
	u.dirty = false
	u.mu.Unlock()
	if err != nil {
		return err
	}
	tmp := u.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, u.path); err != nil {
		return errors.Join(err, os.Remove(tmp))
	}
	return nil
}
