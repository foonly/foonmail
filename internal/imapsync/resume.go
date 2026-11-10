package imapsync

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// Waker tells every subscribed connection to reconnect now. Watch calls
// Wake on resume from suspend; the daemon also calls it when the network
// comes up and when the user asks for a sync.
//
// Go's monotonic clock uses CLOCK_MONOTONIC, which stops during suspend on
// Linux, while the wall clock keeps going; a gap between the two means the
// machine slept. A connection that was idle across a suspend is usually dead
// without either side knowing, so the sessions reconnect at once instead of
// waiting for TCP to notice.
type Waker struct {
	mu   sync.Mutex
	subs []chan struct{}
}

// Subscribe returns a channel that receives a value after each resume.
func (w *Waker) Subscribe() <-chan struct{} {
	ch := make(chan struct{}, 1)
	w.mu.Lock()
	w.subs = append(w.subs, ch)
	w.mu.Unlock()
	return ch
}

// Wake signals every subscriber, without blocking.
func (w *Waker) Wake() {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, ch := range w.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// Watch checks the clocks every few seconds until ctx is cancelled.
func (w *Waker) Watch(ctx context.Context, log *slog.Logger) {
	const tick = 5 * time.Second
	t := time.NewTicker(tick)
	defer t.Stop()
	last := time.Now()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			slept := now.Round(0).Sub(last.Round(0)) - now.Sub(last)
			last = now
			if slept > 10*time.Second {
				log.Info("system resumed from sleep", "slept", slept.Round(time.Second))
				w.Wake()
			}
		}
	}
}
