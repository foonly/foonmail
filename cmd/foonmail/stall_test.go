package main

import (
	"bytes"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestStallWatch checks that an event handled for too long is logged once,
// with the stuck goroutine's stack, and that its end is logged too.
func TestStallWatch(t *testing.T) {
	var logBuf bytes.Buffer
	var mu sync.Mutex
	var reports []string
	s := &stallWatch{
		limit: 50 * time.Millisecond,
		log:   slog.New(slog.NewTextHandler(syncWriter{&logBuf, &mu}, nil)),
		report: func(stacks []byte) {
			mu.Lock()
			reports = append(reports, string(stacks))
			mu.Unlock()
		},
	}
	stop := make(chan struct{})
	defer close(stop)
	go s.watch(10*time.Millisecond, stop)

	// A quick event isn't reported.
	s.begin("app.ConfigEvent")
	s.end()
	time.Sleep(100 * time.Millisecond)

	s.begin("app.FrameEvent")
	stuckInFrame(300 * time.Millisecond)
	s.end()

	mu.Lock()
	defer mu.Unlock()
	if len(reports) != 1 {
		t.Fatalf("%d reports, want 1", len(reports))
	}
	if !strings.Contains(reports[0], "stuckInFrame") {
		t.Errorf("the stacks don't show where it was stuck:\n%s", reports[0])
	}
	out := logBuf.String()
	for _, want := range []string{"the event loop is stuck", "event=app.FrameEvent", "the event loop is running again"} {
		if !strings.Contains(out, want) {
			t.Errorf("log lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "ConfigEvent") {
		t.Errorf("the quick event was logged:\n%s", out)
	}
}

//go:noinline
func stuckInFrame(d time.Duration) { time.Sleep(d) }

type syncWriter struct {
	b  *bytes.Buffer
	mu *sync.Mutex
}

func (w syncWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.Write(p)
}
