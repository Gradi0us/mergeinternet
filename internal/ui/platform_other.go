//go:build !windows

package ui

import (
	"errors"
	"os"
	"os/exec"
	"runtime"
)

func isElevated() bool { return os.Geteuid() == 0 }

func relaunchElevated(string, bool) error {
	return errors.New("restart mi-client with sudo")
}

func OpenBrowser(url string) {
	cmd := "xdg-open"
	if runtime.GOOS == "darwin" {
		cmd = "open"
	}
	_ = exec.Command(cmd, url).Start()
}

func startWithWindows() bool { return false }

func setStartWithWindows(bool, string) error {
	return errors.New("chỉ hỗ trợ trên Windows")
}

// The system proxy is only managed on Windows, so there is nothing to guard.
func startGuard(string) error { return nil }

func waitProcess(int) error { return errors.New("guard is only supported on Windows") }
