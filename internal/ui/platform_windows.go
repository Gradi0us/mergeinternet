package ui

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

func isElevated() bool { return windows.GetCurrentProcessToken().IsElevated() }

// relaunchElevated starts a new copy of mi-client with the UAC prompt. The new
// copy takes over the dashboard port and, with autoStart, connects at once.
func relaunchElevated(configPath string, autoStart bool) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	a := `ui -takeover -config "` + configPath + `"`
	if autoStart {
		a += " -autostart"
	}
	verb, _ := windows.UTF16PtrFromString("runas")
	file, _ := windows.UTF16PtrFromString(exe)
	args, _ := windows.UTF16PtrFromString(a)
	cwd, _ := windows.UTF16PtrFromString(filepath.Dir(exe))
	return windows.ShellExecute(0, verb, file, args, cwd, windows.SW_NORMAL)
}

func OpenBrowser(url string) {
	_ = exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
}

// Start with Windows uses the per-user Run key: no admin rights needed. The
// copy started at logon runs hidden; opening the exe again shows the dashboard.
const runKey = `Software\Microsoft\Windows\CurrentVersion\Run`
const runValue = "mergeinternet"

func startWithWindows() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	_, _, err = k.GetStringValue(runValue)
	return err == nil
}

func setStartWithWindows(on bool, configPath string) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if !on {
		if err := k.DeleteValue(runValue); err != nil && err != registry.ErrNotExist {
			return err
		}
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return k.SetStringValue(runValue, `"`+exe+`" ui -no-browser -hidden -config "`+configPath+`"`)
}

// startGuard launches "mi-client guard" detached from this process and its
// console, so it outlives a kill or a closed console window.
func startGuard(backupPath string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, "guard", "-pid", strconv.Itoa(os.Getpid()), "-backup", backupPath)
	const flags = windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_NO_WINDOW
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: flags | windows.CREATE_BREAKAWAY_FROM_JOB, HideWindow: true}
	if err := cmd.Start(); err != nil {
		// Not allowed to leave our job object (some terminals): stay in it.
		cmd = exec.Command(exe, "guard", "-pid", strconv.Itoa(os.Getpid()), "-backup", backupPath)
		cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: flags, HideWindow: true}
		if err := cmd.Start(); err != nil {
			return err
		}
	}
	return cmd.Process.Release()
}

func waitProcess(pid int) error {
	h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		return nil // already gone
	}
	if err != nil {
		return err // never restore while it might still be running
	}
	defer windows.CloseHandle(h)
	_, err = windows.WaitForSingleObject(h, windows.INFINITE)
	return err
}
