package imapsync

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"

	"foonmail/internal/cache"
)

// SearchEvent is what Search reports as it goes: Progress, Found or
// Skipped.
type SearchEvent interface{ isSearchEvent() }

// Progress says which folder is being searched now: the N-th of Of, in
// the header pass or the body pass.
type Progress struct {
	FolderID int64
	Folder   string
	N, Of    int
	Body     bool
}

// Found is a batch of server search results: messages in one folder,
// stored in the cache, so they can be listed and opened like any other.
type Found struct {
	FolderID int64
	UIDs     []uint32
	InBody   bool // found by the body pass: the words aren't in From, To or Subject
}

// Skipped is a folder left out of a pass, because the server didn't
// answer in time or refused.
type Skipped struct {
	FolderID int64
	Folder   string
	Body     bool
	Reason   string
}

func (Progress) isSearchEvent() {}
func (Found) isSearchEvent()    {}
func (Skipped) isSearchEvent()  {}

// How long one folder's search may take before it is skipped. A server
// with a full-text index answers in well under a second; without one, a
// body search reads every message, about 5 ms each. Dovecot also makes a
// search wait until new mail is indexed, which can take long when its
// indexers are busy elsewhere.
var (
	headerSearchTimeout = 30 * time.Second
	bodySearchTimeout   = 60 * time.Second
)

// Search looks for messages with all of words on the server, in every
// folder but Trash and Junk (on Gmail, in All Mail only, which holds the
// rest). It runs on a connection of its own, so syncing goes on meanwhile.
//
// It searches From, To and Subject in every folder first, which is quick,
// then the whole message (TEXT) folder by folder. report gets a Progress
// before each folder, a Found with each folder's results once they are in
// the cache (a folder's body results leave out its header results), and a
// Skipped for a folder that took too long or failed; the search goes on
// with the next, on a new connection if the old one was dropped. Search
// returns when done, when ctx is cancelled, or when it can't connect.
func (s *Syncer) Search(ctx context.Context, words []string, report func(SearchEvent)) error {
	if len(words) == 0 {
		return nil
	}
	folders, err := s.searchFolders(ctx)
	if err != nil {
		return err
	}
	var c *imapclient.Client
	stop := func() bool { return false }
	drop := func() {
		if c != nil {
			stop()
			c.Close()
			c = nil
		}
	}
	defer drop()

	byHeader := map[int64][]uint32{}
	passes := []struct {
		crit    *imap.SearchCriteria
		body    bool
		timeout time.Duration
	}{
		{headerCriteria(words), false, headerSearchTimeout},
		{&imap.SearchCriteria{Text: words}, true, bodySearchTimeout},
	}
	for _, pass := range passes {
		for i, f := range folders {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if c == nil {
				if c, err = s.connect(nil); err != nil {
					c = nil
					return fmt.Errorf("%w: %v", ErrOffline, err)
				}
				// Cancelling closes the connection, so a long SEARCH returns.
				conn := c
				stop = context.AfterFunc(ctx, func() { conn.Close() })
			}
			report(Progress{FolderID: f.ID, Folder: f.Name, N: i + 1, Of: len(folders), Body: pass.body})
			uids, err := s.searchFolder(ctx, c, f, pass.crit, pass.timeout)
			if err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				reason := err.Error()
				if errors.Is(err, errNoResponse) {
					reason = fmt.Sprintf("no answer in %s", pass.timeout)
				}
				s.log.Warn("search skipped a folder", "folder", f.Name, "body", pass.body, "err", err)
				report(Skipped{FolderID: f.ID, Folder: f.Name, Body: pass.body, Reason: reason})
				select {
				case <-c.Closed():
					drop() // reconnect for the next folder
				default:
				}
				continue
			}
			if pass.body {
				uids = slices.DeleteFunc(uids, func(u uint32) bool { return slices.Contains(byHeader[f.ID], u) })
			} else {
				byHeader[f.ID] = uids
			}
			if len(uids) > 0 {
				report(Found{FolderID: f.ID, UIDs: uids, InBody: pass.body})
			}
		}
	}
	return nil
}

// searchFolders lists the folders to search, likeliest first: INBOX,
// Sent, then the rest by name from the end, which puts yearly archives
// newest first.
func (s *Syncer) searchFolders(ctx context.Context) ([]cache.Folder, error) {
	all, err := s.db.Folders(ctx, s.accountID)
	if err != nil {
		return nil, err
	}
	if i := slices.IndexFunc(all, func(f cache.Folder) bool { return f.SpecialUse == `\All` }); i >= 0 {
		return all[i : i+1], nil
	}
	folders := slices.DeleteFunc(all, func(f cache.Folder) bool {
		return f.SpecialUse == `\Trash` || f.SpecialUse == `\Junk`
	})
	rank := func(f cache.Folder) int {
		switch {
		case f.Name == "INBOX":
			return 0
		case f.SpecialUse == `\Sent`:
			return 1
		}
		return 2
	}
	slices.SortStableFunc(folders, func(a, b cache.Folder) int {
		if d := cmp.Compare(rank(a), rank(b)); d != 0 {
			return d
		}
		return strings.Compare(b.Name, a.Name)
	})
	return folders, nil
}

// headerCriteria matches messages with every word in From, To or Subject.
func headerCriteria(words []string) *imap.SearchCriteria {
	field := func(key, w string) imap.SearchCriteria {
		return imap.SearchCriteria{Header: []imap.SearchCriteriaHeaderField{{Key: key, Value: w}}}
	}
	crit := &imap.SearchCriteria{}
	for _, w := range words {
		crit.And(&imap.SearchCriteria{Or: [][2]imap.SearchCriteria{{
			field("From", w),
			{Or: [][2]imap.SearchCriteria{{field("To", w), field("Subject", w)}}},
		}}})
	}
	return crit
}

// searchFolder searches one folder, read-only, and adds the results the
// cache doesn't have yet, so a folder that was never synced gets just
// the messages found.
func (s *Syncer) searchFolder(ctx context.Context, c *imapclient.Client, f cache.Folder,
	crit *imap.SearchCriteria, timeout time.Duration) ([]uint32, error) {
	sel, err := selectFolder(c, f.Name, true, false)
	if err != nil {
		return nil, err
	}
	if sel.NumMessages == 0 {
		return nil, nil
	}
	var data *imap.SearchData
	err = doWithin(c, "search "+f.Name, timeout, func() (err error) {
		data, err = c.UIDSearch(crit, nil).Wait()
		return err
	})
	if err != nil {
		return nil, err
	}
	var uids []uint32
	for _, u := range data.AllUIDs() {
		uids = append(uids, uint32(u))
	}
	if len(uids) == 0 {
		return nil, nil
	}
	slices.Sort(uids)

	// The cache's copy must belong to the same UIDVALIDITY.
	switch {
	case f.UIDValidity == 0:
		// Never synced: record which UIDVALIDITY the stored results are
		// from, with no modseq, so the first real sync fetches every flag.
		if err := s.db.SaveSyncState(ctx, f.ID, sel.UIDValidity, 0, 0); err != nil {
			return nil, err
		}
	case f.UIDValidity != sel.UIDValidity:
		s.log.Warn("UIDVALIDITY changed, refetching folder", "folder", f.Name)
		if err := s.db.ResetFolder(ctx, f.ID, sel.UIDValidity); err != nil {
			return nil, err
		}
	}
	known, err := s.db.UIDs(ctx, f.ID)
	if err != nil {
		return nil, err
	}
	missing := slices.DeleteFunc(slices.Clone(uids), func(u uint32) bool {
		_, ok := slices.BinarySearch(known, u)
		return ok
	})
	for chunk := range slices.Chunk(missing, fetchChunk) {
		msgs, err := s.fetchMessages(c, imap.UIDSetNum(toUIDs(chunk)...), false)
		if err != nil {
			return nil, err
		}
		if err := s.db.AddMessages(ctx, f.ID, msgs); err != nil {
			return nil, err
		}
	}
	if len(missing) > 0 {
		s.notify(FolderChanged{Account: s.acct.Name, FolderID: f.ID})
	}
	return uids, nil
}
