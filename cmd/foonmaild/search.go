package main

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"

	"foonmail/internal/imapsync"
	"foonmail/internal/rpc"
)

// searchTimeout bounds a whole server search, all folders and both passes.
const searchTimeout = 10 * time.Minute

// searches are the server searches running, so CancelSearch can stop one.
type searches struct {
	mu      sync.Mutex
	next    int64
	running map[int64]context.CancelFunc
}

func (ss *searches) start() (int64, context.Context) {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), searchTimeout)
	ss.next++
	if ss.running == nil {
		ss.running = map[int64]context.CancelFunc{}
	}
	ss.running[ss.next] = cancel
	return ss.next, ctx
}

func (ss *searches) stop(id int64) {
	ss.mu.Lock()
	cancel := ss.running[id]
	delete(ss.running, id)
	ss.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// SearchServer starts a server search of an account for messages with
// every one of words and returns its id at once. SearchProgress, SearchFound
// and SearchSkipped signals follow, then one SearchDone.
func (m methods) SearchServer(account string, words []string) (int64, *dbus.Error) {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	accountID, err := m.s.db.AccountID(ctx, account)
	if err != nil {
		return 0, dbusError(fmt.Errorf("account %s: %w", account, imapsync.ErrNotFound))
	}
	b := m.s.backends[accountID]
	if b == nil {
		return 0, dbusError(fmt.Errorf("account %s isn't configured: %w", account, imapsync.ErrNotFound))
	}
	id, sctx := m.s.searches.start()
	m.s.log.Info("server search", "account", account, "id", id, "words", len(words))
	go func() {
		defer m.s.searches.stop(id)
		started := time.Now()
		n := 0
		err := b.Search(sctx, words, func(ev imapsync.SearchEvent) {
			switch ev := ev.(type) {
			case imapsync.Progress:
				m.s.emit(rpc.SignalSearchProgress, id, ev.FolderID, ev.Folder, int32(ev.N), int32(ev.Of), ev.Body)
			case imapsync.Found:
				n += len(ev.UIDs)
				m.s.emit(rpc.SignalSearchFound, id, ev.FolderID, ev.UIDs, ev.InBody)
			case imapsync.Skipped:
				m.s.emit(rpc.SignalSearchSkipped, id, ev.FolderID, ev.Folder, ev.Body, ev.Reason)
			}
		})
		msg := ""
		if err != nil {
			msg = err.Error()
			if sctx.Err() == context.Canceled {
				msg = "cancelled"
			}
		}
		m.s.log.Info("server search done", "id", id, "found", n, "took", time.Since(started).Round(time.Millisecond), "err", msg)
		m.s.emit(rpc.SignalSearchDone, id, msg)
	}()
	return id, nil
}

// CancelSearch stops a server search; it still ends with SearchDone.
func (m methods) CancelSearch(id int64) *dbus.Error {
	m.s.searches.stop(id)
	return nil
}

func (s *service) emit(signal string, args ...any) {
	if err := s.conn.Emit(rpc.Path, signal, args...); err != nil {
		s.log.Warn("emit "+strings.TrimPrefix(signal, rpc.Interface+"."), "err", err)
	}
}
