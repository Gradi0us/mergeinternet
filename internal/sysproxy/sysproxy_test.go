package sysproxy

import (
	"path/filepath"
	"testing"
)

// fakeOS stands in for WinINet so tests never touch the real system proxy.
type fakeOS struct{ cur Settings }

func newFake(t *testing.T, start Settings) (*Manager, *fakeOS) {
	f := &fakeOS{cur: start}
	m := &Manager{BackupPath: filepath.Join(t.TempDir(), "backup.json"),
		get: func() (Settings, error) { return f.cur, nil },
		set: func(s Settings) error { f.cur = s; return nil }}
	return m, f
}

var userPAC = Settings{Flags: flagDirect | flagAutoURL, AutoConfigURL: "http://corp/proxy.pac"}

func TestInstallThenRestoreGivesBackUserSettings(t *testing.T) {
	m, f := newFake(t, userPAC)
	if err := m.Install("127.0.0.1:7879"); err != nil {
		t.Fatal(err)
	}
	if f.cur.ProxyServer != "127.0.0.1:7879" || f.cur.Flags&flagProxy == 0 || f.cur.Bypass != Bypass {
		t.Fatalf("installed = %+v", f.cur)
	}
	if err := m.Restore(); err != nil {
		t.Fatal(err)
	}
	if f.cur != userPAC {
		t.Fatalf("restored = %+v, want %+v", f.cur, userPAC)
	}
	if err := m.Restore(); err != nil { // idempotent
		t.Fatal(err)
	}
}

func TestRecoverAfterCrash(t *testing.T) {
	m, f := newFake(t, userPAC)
	m.Install("127.0.0.1:7879")
	// crash: no Restore. Next start:
	did, err := m.Recover()
	if err != nil || !did || f.cur != userPAC {
		t.Fatalf("recover did=%v err=%v cur=%+v", did, err, f.cur)
	}
	if did, _ := m.Recover(); did {
		t.Fatal("second recover should be a no-op")
	}
}

func TestRecoverLeavesUserChangeAlone(t *testing.T) {
	m, f := newFake(t, userPAC)
	m.Install("127.0.0.1:7879")
	userNew := Settings{Flags: flagDirect | flagProxy, ProxyServer: "10.0.0.5:3128"}
	f.cur = userNew // the user set another proxy after our crash
	if did, _ := m.Recover(); did || f.cur != userNew {
		t.Fatalf("recover overwrote the user's own proxy: %+v", f.cur)
	}
}

func TestInstallAfterCrashKeepsOriginalBackup(t *testing.T) {
	m, f := newFake(t, userPAC)
	m.Install("127.0.0.1:7879")
	// crashed, then started again without Recover: Install must not back up our own proxy
	m.Install("127.0.0.1:7879")
	m.Restore()
	if f.cur != userPAC {
		t.Fatalf("restored %+v, want the user's original %+v", f.cur, userPAC)
	}
}

// Reads (never writes) the real WinINet settings to check the struct layout
// and string handling against the actual API.
func TestRealReadOnly(t *testing.T) {
	s, err := Current()
	if err == ErrUnsupported {
		t.Skip("not Windows")
	}
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}
	if s.Flags&flagDirect == 0 && s.Flags&(flagProxy|flagAutoURL|flagAutoDetect) == 0 {
		t.Fatalf("implausible flags %#x", s.Flags)
	}
	t.Logf("current system proxy: %+v", s)
}
