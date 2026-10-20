package ui

import (
	"errors"
	"log"

	"mergeinternet/internal/sysproxy"
)

// RunGuard waits for the dashboard process pid to exit, then restores the
// system proxy if it still points at us. After a clean exit the proxy was
// already restored and Recover does nothing; after a kill or crash this keeps
// Windows from pointing at a proxy nobody listens on.
func RunGuard(pid int, backupPath string) error {
	if pid <= 0 || backupPath == "" {
		return errors.New("guard: -pid and -backup are required")
	}
	if err := waitProcess(pid); err != nil {
		return err
	}
	did, err := sysproxy.New(backupPath).Recover()
	if did {
		log.Printf("guard: process %d exited without cleanup, system proxy restored", pid)
	}
	return err
}
