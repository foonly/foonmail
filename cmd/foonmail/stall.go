package main

import (
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"
)

// stallWatch notices when the window's event loop is stuck handling one
// event. While it is, the window doesn't answer the compositor's pings and
// Hyprland offers to kill it, so a stall is logged with every goroutine's
// stack to show where it was stuck.
type stallWatch struct {
	limit  time.Duration
	log    *slog.Logger
	report func(stacks []byte) // also given the stacks; nil writes stallFile

	mu       sync.Mutex
	since    time.Time // when handling the current event began; zero when idle
	what     string    // the event being handled
	reported bool      // this stall was already logged
}

// begin marks the start of handling an event, end its end.
func (s *stallWatch) begin(what string) {
	s.mu.Lock()
	s.since, s.what, s.reported = time.Now(), what, false
	s.mu.Unlock()
}

func (s *stallWatch) end() {
	s.mu.Lock()
	since, what, reported := s.since, s.what, s.reported
	s.since = time.Time{}
	s.mu.Unlock()
	if reported {
		s.log.Warn("the event loop is running again", "event", what, "stalled", time.Since(since).Round(time.Millisecond))
	}
}

// watch checks every tick until stop is closed.
func (s *stallWatch) watch(tick time.Duration, stop <-chan struct{}) {
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			s.check()
		}
	}
}

func (s *stallWatch) check() {
	s.mu.Lock()
	since, what := s.since, s.what
	stalled := !since.IsZero() && !s.reported && time.Since(since) > s.limit
	if stalled {
		s.reported = true
	}
	s.mu.Unlock()
	if !stalled {
		return
	}
	buf := make([]byte, 1<<20)
	buf = buf[:runtime.Stack(buf, true)]
	attrs := []any{"event", what, "for", time.Since(since).Round(time.Millisecond)}
	if s.report != nil {
		s.report(buf)
	} else if path, err := writeStallFile(buf); err == nil {
		attrs = append(attrs, "stacks", path)
	}
	s.log.Warn("the event loop is stuck", append(attrs, "goroutines", string(buf))...)
}

// writeStallFile keeps the latest stall's stacks in the cache directory,
// where they are easy to find whichever way the window was started.
func writeStallFile(stacks []byte) (string, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, "foonmail", "gui-stall.txt")
	head := "foonmail event loop stuck at " + time.Now().Format(time.RFC3339) + "\n\n"
	return path, os.WriteFile(path, append([]byte(head), stacks...), 0o600)
}
