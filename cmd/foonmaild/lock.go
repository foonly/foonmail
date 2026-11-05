package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// errLocked means another foonmaild already uses the cache directory.
var errLocked = errors.New("another foonmaild is already running")

// acquireLock makes sure only one daemon uses a cache directory. The lock
// is an flock on a file in that directory, released by the kernel when the
// process exits, however it exits.
func acquireLock(dir string) (*os.File, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "foonmaild.lock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			pid, _ := os.ReadFile(path)
			return nil, fmt.Errorf("%w: pid %s is using %s", errLocked, strings.TrimSpace(string(pid)), dir)
		}
		return nil, fmt.Errorf("locking %s: %w", path, err)
	}
	f.Truncate(0)
	f.WriteAt([]byte(strconv.Itoa(os.Getpid())+"\n"), 0)
	return f, nil
}
