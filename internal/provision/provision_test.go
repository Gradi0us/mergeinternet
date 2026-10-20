package provision

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"mergeinternet/deploy"
)

const testPassword = "secret"

var testKey = base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))

type fakeOpts struct {
	user        string // account that may log in (password "secret")
	uid         string // output of id -u
	uname       string
	setupOut    string
	setupExit   int
	setupBlock  bool          // setup script blocks until test end
	setupHit    chan struct{} // closed when setup starts (if non-nil)
	sudoNoPass  bool
	noSudo      bool // sudo -S always refuses
	activeReply string
}

type fakeServer struct {
	t        *testing.T
	addr     string
	port     int
	hostSig  ssh.Signer
	opts     fakeOpts
	release  chan struct{}
	mu       sync.Mutex
	cmds     []string
	files    map[string][]byte
	sudoPass []string // first stdin line of each `sudo -S` command
}

func newFake(t *testing.T, o fakeOpts) *fakeServer {
	t.Helper()
	if o.user == "" {
		o.user = "root"
	}
	if o.uid == "" {
		o.uid = "0"
	}
	if o.uname == "" {
		o.uname = "Linux x86_64"
	}
	if o.setupOut == "" && o.setupExit == 0 {
		o.setupOut = "installing...\nMI_KEY=" + testKey + "\nMI_PORT=51820\nMI_PUBLIC_IP=203.0.113.10\n"
	}
	if o.activeReply == "" {
		o.activeReply = "active"
	}
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	hostSig, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeServer{t: t, hostSig: hostSig, opts: o, release: make(chan struct{}), files: map[string][]byte{}}
	f.addr = ln.Addr().String()
	f.port = ln.Addr().(*net.TCPAddr).Port
	cfg := &ssh.ServerConfig{
		PasswordCallback: func(c ssh.ConnMetadata, pw []byte) (*ssh.Permissions, error) {
			if c.User() == o.user && string(pw) == testPassword {
				return nil, nil
			}
			return nil, errors.New("denied")
		},
	}
	cfg.AddHostKey(hostSig)
	t.Cleanup(func() { close(f.release); ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(c, cfg)
		}
	}()
	return f
}

func (f *fakeServer) fingerprint() string { return ssh.FingerprintSHA256(f.hostSig.PublicKey()) }

func (f *fakeServer) serve(c net.Conn, cfg *ssh.ServerConfig) {
	defer c.Close()
	sc, chans, reqs, err := ssh.NewServerConn(c, cfg)
	if err != nil {
		return
	}
	defer sc.Close()
	go ssh.DiscardRequests(reqs)
	for nc := range chans {
		if nc.ChannelType() != "session" {
			nc.Reject(ssh.UnknownChannelType, "no")
			continue
		}
		ch, creqs, err := nc.Accept()
		if err != nil {
			return
		}
		go func() {
			for r := range creqs {
				if r.Type != "exec" {
					r.Reply(false, nil)
					continue
				}
				var p struct{ Cmd string }
				ssh.Unmarshal(r.Payload, &p)
				r.Reply(true, nil)
				go func() {
					code := f.handle(p.Cmd, ch)
					ch.SendRequest("exit-status", false, ssh.Marshal(struct{ C uint32 }{uint32(code)}))
					ch.Close()
				}()
				return
			}
		}()
	}
}

// handle emulates a shell for the exact commands the provisioner sends.
func (f *fakeServer) handle(cmd string, ch ssh.Channel) int {
	f.mu.Lock()
	f.cmds = append(f.cmds, cmd)
	f.mu.Unlock()
	out := func(s string) int { io.WriteString(ch, s); return 0 }
	br := bufio.NewReader(ch)

	inner := cmd
	if rest, ok := strings.CutPrefix(cmd, "sudo -S -p '' "); ok {
		line, _ := br.ReadString('\n')
		line = strings.TrimSuffix(line, "\n")
		f.mu.Lock()
		f.sudoPass = append(f.sudoPass, line)
		f.mu.Unlock()
		if line != testPassword || f.opts.noSudo {
			io.WriteString(ch.Stderr(), "Sorry, try again.\n")
			return 1
		}
		inner = rest
	} else if rest, ok := strings.CutPrefix(cmd, "sudo -n "); ok {
		if !f.opts.sudoNoPass {
			io.WriteString(ch.Stderr(), "sudo: a password is required\n")
			return 1
		}
		inner = rest
	}

	switch {
	case inner == "true":
		return 0
	case inner == "uname -sm":
		return out(f.opts.uname + "\n")
	case strings.Contains(inner, "/etc/os-release"):
		return out("ubuntu\n")
	case inner == "id -u":
		return out(f.opts.uid + "\n")
	case strings.Contains(inner, "command -v systemctl"), strings.Contains(inner, "command -v iptables"):
		return out("yes\n")
	case inner == "mktemp -d":
		return out("/tmp/mi.XXXX\n")
	case strings.HasPrefix(inner, "cat > "):
		path := strings.Trim(strings.TrimPrefix(inner, "cat > "), "'")
		data, _ := io.ReadAll(br)
		f.mu.Lock()
		f.files[path] = data
		f.mu.Unlock()
		return 0
	case strings.HasPrefix(inner, "chmod +x "), strings.HasPrefix(inner, "rm -rf "):
		return 0
	case strings.HasPrefix(inner, "sha256sum "):
		var sb strings.Builder
		for _, p := range strings.Fields(strings.TrimPrefix(inner, "sha256sum ")) {
			p = strings.Trim(p, "'")
			f.mu.Lock()
			h := sha256.Sum256(f.files[p])
			f.mu.Unlock()
			fmt.Fprintf(&sb, "%s  %s\n", hex.EncodeToString(h[:]), p)
		}
		return out(sb.String())
	case strings.HasPrefix(inner, "env PORT=") && strings.Contains(inner, "bash"):
		if f.opts.setupHit != nil {
			close(f.opts.setupHit)
		}
		if f.opts.setupBlock {
			<-f.release
			return 1
		}
		io.WriteString(ch, f.opts.setupOut)
		return f.opts.setupExit
	case inner == "systemctl is-active mi-server":
		return out(f.opts.activeReply + "\n")
	}
	io.WriteString(ch.Stderr(), "fake: unknown command: "+cmd+"\n")
	return 127
}

func (f *fakeServer) file(path string) []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.files[path]
}

func (f *fakeServer) commands() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.cmds...)
}

// ---- helpers ----

func writeBin(t *testing.T, content string) func(string) (string, error) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "mi-server")
	if err := os.WriteFile(p, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	return func(string) (string, error) { return p, nil }
}

func baseReq(f *fakeServer, t *testing.T) Request {
	return Request{
		Host: "127.0.0.1", SSHPort: f.port, User: f.opts.user, Password: testPassword,
		ServerBinary: writeBin(t, "ELF-fake-binary"),
	}
}

func withTimeout(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	return ctx
}

type stepLog struct {
	mu    sync.Mutex
	steps []Step
}

func (s *stepLog) add(st Step) { s.mu.Lock(); s.steps = append(s.steps, st); s.mu.Unlock() }
func (s *stepLog) last(id string) Step {
	s.mu.Lock()
	defer s.mu.Unlock()
	var r Step
	for _, st := range s.steps {
		if st.ID == id {
			r = st
		}
	}
	return r
}

// ---- tests ----

func TestRunHappyPathRoot(t *testing.T) {
	f := newFake(t, fakeOpts{})
	var log stepLog
	req := baseReq(f, t)
	binPath, _ := req.ServerBinary("amd64")

	res, err := Run(withTimeout(t), req, log.add)
	if err != nil {
		t.Fatal(err)
	}
	if res.ServerAddr != "127.0.0.1:51820" || res.Key != testKey || res.Arch != "amd64" || res.Distro != "ubuntu" {
		t.Fatalf("unexpected result %+v", res)
	}
	if res.HostKeyFingerprint != f.fingerprint() {
		t.Fatalf("fingerprint %q != %q", res.HostKeyFingerprint, f.fingerprint())
	}
	want, _ := os.ReadFile(binPath)
	if !bytes.Equal(f.file("/tmp/mi.XXXX/mi-server"), want) {
		t.Fatal("uploaded binary differs")
	}
	if !bytes.Equal(f.file("/tmp/mi.XXXX/server-setup.sh"), deploy.ServerSetup) || len(deploy.ServerSetup) == 0 {
		t.Fatal("uploaded script differs from embedded script")
	}
	// step order: first "running" of each step, then every step done.
	var order []string
	seen := map[string]bool{}
	for _, st := range log.steps {
		if !seen[st.ID] {
			seen[st.ID] = true
			order = append(order, st.ID)
			if st.Status != "running" {
				t.Fatalf("first event of %s is %s", st.ID, st.Status)
			}
		}
	}
	if strings.Join(order, ",") != "connect,detect,upload,install,verify" {
		t.Fatalf("step order %v", order)
	}
	for _, id := range order {
		if log.last(id).Status != "done" {
			t.Fatalf("step %s not done: %+v", id, log.last(id))
		}
	}
	// installed with root: no sudo anywhere, PORT passed.
	var sawInstall bool
	for _, c := range f.commands() {
		if strings.Contains(c, "sudo") {
			t.Fatalf("root path used sudo: %q", c)
		}
		if strings.HasPrefix(c, "env PORT=51820 bash ") {
			sawInstall = true
		}
	}
	if !sawInstall {
		t.Fatalf("install command missing: %v", f.commands())
	}
}

func TestRunNonRootUsesSudoWithPasswordOnStdin(t *testing.T) {
	f := newFake(t, fakeOpts{user: "ubuntu", uid: "1000"})
	var log stepLog
	res, err := Run(withTimeout(t), baseReq(f, t), log.add)
	if err != nil {
		t.Fatal(err)
	}
	if res.Key != testKey {
		t.Fatalf("result %+v", res)
	}
	cmds := f.commands()
	var sudoInstall bool
	for _, c := range cmds {
		if strings.Contains(c, testPassword) {
			t.Fatalf("password leaked into command: %q", c)
		}
		if strings.HasPrefix(c, "sudo -S -p '' env PORT=51820 bash ") {
			sudoInstall = true
		}
	}
	if !sudoInstall {
		t.Fatalf("install not run via sudo -S: %v", cmds)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sudoPass) < 2 {
		t.Fatalf("expected password on stdin for sudo check + install, got %v", f.sudoPass)
	}
	for _, p := range f.sudoPass {
		if p != testPassword {
			t.Fatalf("sudo stdin line %q", p)
		}
	}
}

func TestRunNonRootNoSudoRights(t *testing.T) {
	f := newFake(t, fakeOpts{user: "ubuntu", uid: "1000", noSudo: true})
	var log stepLog
	_, err := Run(withTimeout(t), baseReq(f, t), log.add)
	if err == nil || err.Error() != "Tài khoản cần quyền root hoặc sudo." {
		t.Fatalf("err = %v", err)
	}
	if log.last("detect").Status != "failed" || log.last("upload").ID != "" {
		t.Fatalf("steps: %+v", log.steps)
	}
}

func TestRunWrongPassword(t *testing.T) {
	f := newFake(t, fakeOpts{})
	req := baseReq(f, t)
	req.Password = "nope"
	var log stepLog
	_, err := Run(withTimeout(t), req, log.add)
	if err == nil || err.Error() != "Sai tài khoản hoặc mật khẩu SSH." {
		t.Fatalf("err = %v", err)
	}
	if st := log.last("connect"); st.Status != "failed" || st.Detail != err.Error() {
		t.Fatalf("connect step %+v", st)
	}
	if strings.Contains(err.Error(), "nope") {
		t.Fatal("password in error")
	}
}

func TestRunWrongUser(t *testing.T) {
	f := newFake(t, fakeOpts{})
	req := baseReq(f, t)
	req.User = "admin"
	_, err := Run(withTimeout(t), req, nil)
	if err == nil || !strings.Contains(err.Error(), "Sai tài khoản hoặc mật khẩu") {
		t.Fatalf("err = %v", err)
	}
}

func TestRunConnectionRefused(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	req := Request{Host: "127.0.0.1", SSHPort: port, Password: "x", ServerBinary: writeBin(t, "x")}
	_, err := Run(withTimeout(t), req, nil)
	if err == nil || !strings.Contains(err.Error(), "Không kết nối được tới VPS 127.0.0.1:"+strconv.Itoa(port)) {
		t.Fatalf("err = %v", err)
	}
}

func TestRunHostKeyPinning(t *testing.T) {
	f := newFake(t, fakeOpts{})
	req := baseReq(f, t)
	req.KnownHostKey = "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	var log stepLog
	_, err := Run(withTimeout(t), req, log.add)
	if err == nil || !strings.Contains(err.Error(), "CẢNH BÁO") {
		t.Fatalf("expected host key warning, got %v", err)
	}
	if log.last("connect").Status != "failed" {
		t.Fatal("connect should be failed")
	}
	if len(f.commands()) != 0 {
		t.Fatalf("commands ran despite mismatch: %v", f.commands())
	}
	req.KnownHostKey = f.fingerprint()
	if _, err := Run(withTimeout(t), req, nil); err != nil {
		t.Fatalf("matching pin should pass: %v", err)
	}
}

func TestRunArm64AsksForArm64Binary(t *testing.T) {
	f := newFake(t, fakeOpts{uname: "Linux aarch64"})
	req := baseReq(f, t)
	inner := writeBin(t, "arm-binary")
	var asked []string
	req.ServerBinary = func(arch string) (string, error) { asked = append(asked, arch); return inner(arch) }
	res, err := Run(withTimeout(t), req, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Arch != "arm64" || len(asked) != 1 || asked[0] != "arm64" {
		t.Fatalf("arch %q asked %v", res.Arch, asked)
	}
	if string(f.file("/tmp/mi.XXXX/mi-server")) != "arm-binary" {
		t.Fatal("wrong binary uploaded")
	}
}

func TestRunUnsupportedArchAndOS(t *testing.T) {
	for _, tc := range []struct{ uname, want string }{
		{"Linux armv7l", "Kiến trúc armv7l chưa hỗ trợ"},
		{"Darwin arm64", "phải chạy Linux"},
	} {
		f := newFake(t, fakeOpts{uname: tc.uname})
		var log stepLog
		_, err := Run(withTimeout(t), baseReq(f, t), log.add)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: err = %v", tc.uname, err)
		}
		if log.last("detect").Status != "failed" {
			t.Fatalf("%s: detect not failed", tc.uname)
		}
	}
}

func TestRunSetupScriptFails(t *testing.T) {
	var sb strings.Builder
	for i := 1; i <= 30; i++ {
		fmt.Fprintf(&sb, "line %d\n", i)
	}
	sb.WriteString("boom: iptables exploded\n")
	f := newFake(t, fakeOpts{setupOut: sb.String(), setupExit: 1})
	var log stepLog
	_, err := Run(withTimeout(t), baseReq(f, t), log.add)
	if err == nil {
		t.Fatal("expected error")
	}
	st := log.last("install")
	if st.Status != "failed" || !strings.Contains(st.Detail, "boom: iptables exploded") || !strings.Contains(st.Detail, "line 30") {
		t.Fatalf("install step %+v", st)
	}
	if strings.Contains(st.Detail, "line 1\n") || strings.Contains(st.Detail, "line 10\n") {
		t.Fatalf("detail should only contain the tail: %q", st.Detail)
	}
	if log.last("verify").ID != "" {
		t.Fatal("verify must not start after install failure")
	}
}

func TestRunServiceNotActive(t *testing.T) {
	f := newFake(t, fakeOpts{activeReply: "failed"})
	var log stepLog
	_, err := Run(withTimeout(t), baseReq(f, t), log.add)
	if err == nil || log.last("verify").Status != "failed" {
		t.Fatalf("err=%v verify=%+v", err, log.last("verify"))
	}
}

func TestRunCancelDuringInstallReturnsPromptly(t *testing.T) {
	hit := make(chan struct{})
	f := newFake(t, fakeOpts{setupBlock: true, setupHit: hit})
	ctx, cancel := context.WithCancel(withTimeout(t))
	defer cancel()
	errc := make(chan error, 1)
	go func() {
		_, err := Run(ctx, baseReq(f, t), nil)
		errc <- err
	}()
	select {
	case <-hit:
	case <-time.After(10 * time.Second):
		t.Fatal("install never started")
	}
	start := time.Now()
	cancel()
	select {
	case err := <-errc:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v", err)
		}
		if d := time.Since(start); d > 2*time.Second {
			t.Fatalf("took %v", d)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return within 2s of cancel")
	}
}

func TestResolveAddrPrefersPublicIPForHostname(t *testing.T) {
	r := &runner{req: Request{Host: "vps.invalid"}}
	got, err := r.resolveAddr(context.Background(), "203.0.113.10", 51820)
	if err != nil || got != "203.0.113.10:51820" {
		t.Fatalf("got %q err %v", got, err)
	}
}

func TestParseConnectionCode(t *testing.T) {
	addr, key, err := ParseConnectionCode("  mi1:203.0.113.10:51820:" + testKey + " \n")
	if err != nil || addr != "203.0.113.10:51820" || key != testKey {
		t.Fatalf("%q %q %v", addr, key, err)
	}
	for name, code := range map[string]string{
		"bad ip":      "mi1:999.1.1.1:51820:" + testKey,
		"hostname":    "mi1:example.com:51820:" + testKey,
		"ipv6":        "mi1:::1:51820:" + testKey,
		"bad port":    "mi1:203.0.113.10:70000:" + testKey,
		"zero port":   "mi1:203.0.113.10:0:" + testKey,
		"non numeric": "mi1:203.0.113.10:abc:" + testKey,
		"bad key":     "mi1:203.0.113.10:51820:notakey",
		"short key":   "mi1:203.0.113.10:51820:" + base64.StdEncoding.EncodeToString([]byte("short")),
		"bad prefix":  "mi2:203.0.113.10:51820:" + testKey,
		"extra part":  "mi1:203.0.113.10:51820:" + testKey + ":x",
		"empty":       "",
	} {
		if _, _, err := ParseConnectionCode(code); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}
