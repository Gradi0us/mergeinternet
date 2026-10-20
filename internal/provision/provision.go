// Package provision installs mi-server on a Linux VPS over SSH: it uploads the
// binary and the embedded setup script, runs the script and reads back the
// generated key and public address.
package provision

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"mergeinternet/deploy"
)

type Request struct {
	Host         string // IPv4 or hostname of the VPS
	SSHPort      int    // default 22
	User         string // default "root"
	Password     string // used for SSH auth and, if not root, for sudo -S; never logged or stored
	PrivateKey   []byte // optional PEM private key (used instead of / in addition to Password)
	KnownHostKey string // optional SHA256 fingerprint to pin (ssh.FingerprintSHA256 format); empty = trust on first use
	UDPPort      int    // default 51820
	// ServerBinary returns the path of a linux mi-server binary for GOARCH
	// "amd64" or "arm64" (error if missing).
	ServerBinary func(arch string) (string, error)
}

type Result struct {
	ServerAddr         string // "ipv4:port"
	Key                string
	HostKeyFingerprint string // SHA256:... of the server host key
	Arch               string // amd64 | arm64
	Distro             string // from /etc/os-release ID
}

type Step struct {
	ID     string // "connect","detect","upload","install","verify"
	Title  string
	Status string // "running" | "done" | "failed"
	Detail string
}

const (
	stepConnect = "connect"
	stepDetect  = "detect"
	stepUpload  = "upload"
	stepInstall = "install"
	stepVerify  = "verify"

	dialTimeout    = 15 * time.Second
	shortTimeout   = 30 * time.Second
	uploadTimeout  = 3 * time.Minute
	installTimeout = 5 * time.Minute
)

var stepTitles = map[string]string{
	stepConnect: "Kết nối SSH tới VPS",
	stepDetect:  "Kiểm tra hệ điều hành VPS",
	stepUpload:  "Tải mi-server lên VPS",
	stepInstall: "Cài đặt và khởi động dịch vụ",
	stepVerify:  "Kiểm tra dịch vụ đang chạy",
}

// Run provisions the VPS. progress may be nil.
func Run(ctx context.Context, req Request, progress func(Step)) (Result, error) {
	if req.SSHPort == 0 {
		req.SSHPort = 22
	}
	if req.User == "" {
		req.User = "root"
	}
	if req.UDPPort == 0 {
		req.UDPPort = 51820
	}
	if progress == nil {
		progress = func(Step) {}
	}
	r := &runner{req: req, progress: progress}
	return r.run(ctx)
}

type runner struct {
	req      Request
	progress func(Step)
	client   *ssh.Client
	root     bool
	usePass  bool // feed password to `sudo -S`
}

func (r *runner) emit(id, status, detail string) {
	r.progress(Step{ID: id, Title: stepTitles[id], Status: status, Detail: detail})
}

// fail emits the failed step and returns the error.
func (r *runner) fail(id string, err error) error {
	r.emit(id, "failed", err.Error())
	return err
}

func (r *runner) run(ctx context.Context) (res Result, err error) {
	// 1. connect
	r.emit(stepConnect, "running", "")
	fp, err := r.connect(ctx)
	if err != nil {
		return res, r.fail(stepConnect, err)
	}
	defer r.client.Close()
	res.HostKeyFingerprint = fp
	r.emit(stepConnect, "done", "")

	// 2. detect
	r.emit(stepDetect, "running", "")
	needIptables, err := r.detect(ctx, &res)
	if err != nil {
		return res, r.fail(stepDetect, err)
	}
	r.emit(stepDetect, "done", res.Distro+"/"+res.Arch)

	// 3. upload
	r.emit(stepUpload, "running", "")
	dir, err := r.upload(ctx, res.Arch)
	if dir != "" {
		defer r.cleanup(dir)
	}
	if err != nil {
		return res, r.fail(stepUpload, err)
	}
	r.emit(stepUpload, "done", "")

	// 4. install
	r.emit(stepInstall, "running", "")
	key, port, pubIP, err := r.install(ctx, dir, needIptables)
	if err != nil {
		return res, r.fail(stepInstall, err)
	}
	addr, err := r.resolveAddr(ctx, pubIP, port)
	if err != nil {
		return res, r.fail(stepInstall, err)
	}
	res.Key, res.ServerAddr = key, addr
	r.emit(stepInstall, "done", "")

	// 5. verify
	r.emit(stepVerify, "running", "")
	out, _, _, err := r.exec(ctx, shortTimeout, "systemctl is-active mi-server", nil)
	if err != nil {
		return res, r.fail(stepVerify, err)
	}
	if strings.TrimSpace(out) != "active" {
		return res, r.fail(stepVerify, errors.New("Dịch vụ mi-server không chạy sau khi cài (xem: journalctl -u mi-server)."))
	}
	r.cleanup(dir)
	r.emit(stepVerify, "done", "")
	return res, nil
}

// ---- connect ----

type hostKeyMismatch struct{ want, got string }

func (e *hostKeyMismatch) Error() string {
	return "CẢNH BÁO: khóa máy chủ của VPS không khớp với khóa đã lưu (có thể VPS đã cài lại hoặc đang bị nghe lén). Đã dừng để an toàn."
}

func (r *runner) connect(ctx context.Context) (fingerprint string, err error) {
	req := r.req
	var methods []ssh.AuthMethod
	if len(req.PrivateKey) > 0 {
		signer, kerr := ssh.ParsePrivateKey(req.PrivateKey)
		if kerr != nil {
			return "", errors.New("Khóa riêng SSH không hợp lệ.")
		}
		methods = append(methods, ssh.PublicKeys(signer))
	}
	if req.Password != "" {
		pw := req.Password
		methods = append(methods, ssh.Password(pw),
			ssh.KeyboardInteractive(func(_, _ string, qs []string, _ []bool) ([]string, error) {
				ans := make([]string, len(qs))
				for i := range ans {
					ans[i] = pw
				}
				return ans, nil
			}))
	}
	if len(methods) == 0 {
		return "", errors.New("Cần nhập mật khẩu hoặc khóa riêng SSH.")
	}

	var mismatch *hostKeyMismatch
	cfg := &ssh.ClientConfig{
		User: req.User,
		Auth: methods,
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			fingerprint = ssh.FingerprintSHA256(key)
			if req.KnownHostKey != "" && req.KnownHostKey != fingerprint {
				mismatch = &hostKeyMismatch{want: req.KnownHostKey, got: fingerprint}
				return mismatch
			}
			return nil
		},
		Timeout: dialTimeout,
	}

	hostPort := net.JoinHostPort(req.Host, strconv.Itoa(req.SSHPort))
	unreachable := fmt.Errorf("Không kết nối được tới VPS %s (kiểm tra IP, cổng SSH và VPS đã bật chưa).", hostPort)

	dctx, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()
	conn, derr := (&net.Dialer{}).DialContext(dctx, "tcp", hostPort)
	if derr != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", unreachable
	}
	// Bound the handshake too; also abort it when ctx is cancelled.
	conn.SetDeadline(time.Now().Add(dialTimeout))
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	cc, chans, reqs, herr := ssh.NewClientConn(conn, hostPort, cfg)
	stopped := stop()
	if herr != nil {
		conn.Close()
		switch {
		case ctx.Err() != nil:
			return "", ctx.Err()
		case mismatch != nil:
			return "", mismatch
		case strings.Contains(herr.Error(), "unable to authenticate"):
			return "", errors.New("Sai tài khoản hoặc mật khẩu SSH.")
		default:
			return "", unreachable
		}
	}
	if !stopped { // ctx fired right after the handshake
		cc.Close()
		return "", ctx.Err()
	}
	conn.SetDeadline(time.Time{})
	r.client = ssh.NewClient(cc, chans, reqs)
	return fingerprint, nil
}

// ---- remote exec ----

type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

type teeWriter struct{ a, b *lockedBuf }

func (t *teeWriter) Write(p []byte) (int, error) {
	t.a.Write(p)
	return t.b.Write(p)
}

// exec runs cmd with a deadline. It returns stdout, combined output, exit code
// and an error that is non-nil only for transport/timeout/cancel failures.
func (r *runner) exec(ctx context.Context, timeout time.Duration, cmd string, stdin []byte) (stdout, combined string, code int, err error) {
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	sess, err := r.client.NewSession()
	if err != nil {
		if ctx.Err() != nil {
			return "", "", 0, ctx.Err()
		}
		return "", "", 0, fmt.Errorf("Không mở được phiên SSH: %w", err)
	}
	defer sess.Close()
	var out, all lockedBuf
	sess.Stdout = &teeWriter{&out, &all}
	sess.Stderr = &all
	sess.Stdin = bytes.NewReader(stdin)

	done := make(chan error, 1)
	go func() { done <- sess.Run(cmd) }()
	select {
	case rerr := <-done:
		var ee *ssh.ExitError
		switch {
		case rerr == nil:
			return out.String(), all.String(), 0, nil
		case errors.As(rerr, &ee):
			return out.String(), all.String(), ee.ExitStatus(), nil
		case ctx.Err() != nil:
			return out.String(), all.String(), 0, ctx.Err()
		default:
			return out.String(), all.String(), 0, fmt.Errorf("Lệnh trên VPS bị gián đoạn: %w", rerr)
		}
	case <-cctx.Done():
		sess.Close()
		if ctx.Err() != nil {
			return out.String(), all.String(), 0, ctx.Err()
		}
		return out.String(), all.String(), 0, errors.New("Lệnh trên VPS chạy quá thời gian cho phép.")
	}
}

// priv returns the command prefixed for root execution plus the stdin prefix.
func (r *runner) priv(cmd string) (string, []byte) {
	if r.root {
		return cmd, nil
	}
	if r.usePass {
		return "sudo -S -p '' " + cmd, []byte(r.req.Password + "\n")
	}
	return "sudo -n " + cmd, nil
}

func shQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// ---- detect ----

func (r *runner) detect(ctx context.Context, res *Result) (needIptables bool, err error) {
	out, _, _, err := r.exec(ctx, shortTimeout, "uname -sm", nil)
	if err != nil {
		return false, err
	}
	f := strings.Fields(out)
	if len(f) < 2 || f[0] != "Linux" {
		return false, fmt.Errorf("VPS phải chạy Linux (nhận được: %q).", strings.TrimSpace(out))
	}
	switch f[1] {
	case "x86_64", "amd64":
		res.Arch = "amd64"
	case "aarch64", "arm64":
		res.Arch = "arm64"
	default:
		return false, fmt.Errorf("Kiến trúc %s chưa hỗ trợ (chỉ amd64 và arm64).", f[1])
	}

	out, _, _, err = r.exec(ctx, shortTimeout, `. /etc/os-release 2>/dev/null; echo "$ID"`, nil)
	if err != nil {
		return false, err
	}
	res.Distro = strings.TrimSpace(out)

	out, _, _, err = r.exec(ctx, shortTimeout, "id -u", nil)
	if err != nil {
		return false, err
	}
	r.root = strings.TrimSpace(out) == "0"
	if !r.root {
		_, _, c, err := r.exec(ctx, shortTimeout, "sudo -n true", nil)
		if err != nil {
			return false, err
		}
		if c != 0 {
			okPass := false
			if r.req.Password != "" {
				_, _, c, err = r.exec(ctx, shortTimeout, "sudo -S -p '' true", []byte(r.req.Password+"\n"))
				if err != nil {
					return false, err
				}
				okPass = c == 0
				r.usePass = okPass
			}
			if !okPass {
				return false, errors.New("Tài khoản cần quyền root hoặc sudo.")
			}
		}
	}

	out, _, _, err = r.exec(ctx, shortTimeout, "command -v systemctl >/dev/null 2>&1 && echo yes || echo no", nil)
	if err != nil {
		return false, err
	}
	if strings.TrimSpace(out) != "yes" {
		return false, errors.New("VPS không dùng systemd; chưa hỗ trợ.")
	}
	out, _, _, err = r.exec(ctx, shortTimeout, "command -v iptables >/dev/null 2>&1 && echo yes || echo no", nil)
	if err != nil {
		return false, err
	}
	if strings.TrimSpace(out) != "yes" {
		if res.Distro != "debian" && res.Distro != "ubuntu" {
			return false, errors.New("VPS chưa cài iptables; hãy cài iptables rồi thử lại.")
		}
		needIptables = true
	}
	return needIptables, nil
}

// ---- upload ----

func (r *runner) upload(ctx context.Context, arch string) (dir string, err error) {
	if r.req.ServerBinary == nil {
		return "", errors.New("Thiếu tệp mi-server cho Linux.")
	}
	path, err := r.req.ServerBinary(arch)
	if err != nil {
		return "", fmt.Errorf("Không tìm thấy mi-server cho Linux %s: %w", arch, err)
	}
	bin, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("Không đọc được %s: %w", path, err)
	}

	out, _, c, err := r.exec(ctx, shortTimeout, "mktemp -d", nil)
	if err != nil {
		return "", err
	}
	dir = strings.TrimSpace(out)
	if c != 0 || !strings.HasPrefix(dir, "/") {
		return "", errors.New("Không tạo được thư mục tạm trên VPS.")
	}

	files := []struct {
		name string
		data []byte
	}{{"mi-server", bin}, {"server-setup.sh", deploy.ServerSetup}}
	var sums []string
	for _, f := range files {
		p := dir + "/" + f.name
		_, all, c, err := r.exec(ctx, uploadTimeout, "cat > "+shQuote(p), f.data)
		if err != nil {
			return dir, err
		}
		if c != 0 {
			return dir, fmt.Errorf("Không ghi được %s lên VPS: %s", f.name, tail(all, 3))
		}
		h := sha256.Sum256(f.data)
		sums = append(sums, hex.EncodeToString(h[:]))
	}
	_, all, c, err := r.exec(ctx, shortTimeout, "chmod +x "+shQuote(dir+"/mi-server")+" "+shQuote(dir+"/server-setup.sh"), nil)
	if err != nil {
		return dir, err
	}
	if c != 0 {
		return dir, fmt.Errorf("chmod thất bại: %s", tail(all, 3))
	}
	out, all, c, err = r.exec(ctx, shortTimeout, "sha256sum "+shQuote(dir+"/mi-server")+" "+shQuote(dir+"/server-setup.sh"), nil)
	if err != nil {
		return dir, err
	}
	if c != 0 {
		return dir, fmt.Errorf("sha256sum thất bại: %s", tail(all, 3))
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	for i, f := range files {
		got := ""
		if i < len(lines) {
			if fs := strings.Fields(lines[i]); len(fs) > 0 {
				got = fs[0]
			}
		}
		if got != sums[i] {
			return dir, fmt.Errorf("Tệp %s trên VPS không khớp mã băm sau khi tải lên (lỗi đường truyền); hãy thử lại.", f.name)
		}
	}
	return dir, nil
}

// ---- install ----

func (r *runner) install(ctx context.Context, dir string, needIptables bool) (key string, port int, pubIP string, err error) {
	if needIptables {
		cmd, in := r.priv("env DEBIAN_FRONTEND=noninteractive apt-get -y install iptables")
		_, all, c, err := r.exec(ctx, installTimeout, cmd, in)
		if err != nil {
			return "", 0, "", installErr(ctx, err, all)
		}
		if c != 0 {
			return "", 0, "", fmt.Errorf("Không cài được iptables:\n%s", tail(all, 15))
		}
	}

	cmd, in := r.priv("env PORT=" + strconv.Itoa(r.req.UDPPort) + " bash " + shQuote(dir+"/server-setup.sh"))
	out, all, c, err := r.exec(ctx, installTimeout, cmd, in)
	if err != nil {
		return "", 0, "", installErr(ctx, err, all)
	}
	if c != 0 {
		return "", 0, "", fmt.Errorf("Script cài đặt thất bại (mã %d):\n%s", c, tail(all, 15))
	}
	vals := map[string]string{}
	for _, ln := range strings.Split(out, "\n") {
		ln = strings.TrimSpace(ln)
		for _, k := range []string{"MI_KEY", "MI_PORT", "MI_PUBLIC_IP"} {
			if v, ok := strings.CutPrefix(ln, k+"="); ok {
				vals[k] = strings.TrimSpace(v)
			}
		}
	}
	key = vals["MI_KEY"]
	if !validKey(key) {
		return "", 0, "", fmt.Errorf("Script không trả về key hợp lệ:\n%s", tail(all, 15))
	}
	port, perr := strconv.Atoi(vals["MI_PORT"])
	if perr != nil || port < 1 || port > 65535 {
		port = r.req.UDPPort
	}
	return key, port, vals["MI_PUBLIC_IP"], nil
}

func installErr(parent context.Context, err error, all string) error {
	if parent.Err() != nil {
		return parent.Err()
	}
	if strings.Contains(err.Error(), "quá thời gian") {
		return fmt.Errorf("Cài đặt quá thời gian cho phép (5 phút), đã dừng.\n%s", tail(all, 15))
	}
	return fmt.Errorf("%w\n%s", err, tail(all, 15))
}

// resolveAddr returns an IPv4 "ip:port".
func (r *runner) resolveAddr(ctx context.Context, pubIP string, port int) (string, error) {
	hostAddr, hostErr := netip.ParseAddr(r.req.Host)
	if hostErr == nil && hostAddr.Is4() {
		return netip.AddrPortFrom(hostAddr, uint16(port)).String(), nil
	}
	if a, err := netip.ParseAddr(pubIP); err == nil && a.Is4() {
		return netip.AddrPortFrom(a, uint16(port)).String(), nil
	}
	if hostErr != nil { // a hostname: resolve it
		rctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		ips, err := net.DefaultResolver.LookupIPAddr(rctx, r.req.Host)
		if err == nil {
			for _, ip := range ips {
				if a, ok := netip.AddrFromSlice(ip.IP); ok && a.Unmap().Is4() {
					return netip.AddrPortFrom(a.Unmap(), uint16(port)).String(), nil
				}
			}
		}
	}
	return "", errors.New("Không xác định được địa chỉ IPv4 công cộng của VPS.")
}

// cleanup removes the temp dir; best effort with a short fresh deadline.
func (r *runner) cleanup(dir string) {
	if dir == "" || r.client == nil {
		return
	}
	cctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	r.exec(cctx, time.Second, "rm -rf "+shQuote(dir), nil)
}

func tail(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\r\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
