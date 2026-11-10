package imapsync

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"

	"foonmail/internal/cache"
)

// Found is a batch of server search results: messages in one folder,
// stored in the cache, so they can be listed and opened like any other.
type Found struct {
	FolderID int64
	UIDs     []uint32
	InBody   bool // found by the body pass: the words aren't in From, To or Subject
}

// Search looks for messages with all of words on the server, in every
// folder but Trash and Junk (on Gmail, in All Mail only, which holds the
// rest). It runs on a connection of its own, so syncing goes on meanwhile.
//
// It searches From, To and Subject in every folder first, which is quick,
// then the whole message (TEXT) folder by folder, which without a
// full-text index on the server means reading every message. found is
// called with each folder's results as they come, after they are in the
// cache; a folder's body results leave out its header results. Search
// returns when done or when ctx is cancelled.
func (s *Syncer) Search(ctx context.Context, words []string, found func(Found)) error {
	if len(words) == 0 {
		return nil
	}
	folders, err := s.searchFolders(ctx)
	if err != nil {
		return err
	}
	c, err := s.connect(nil)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrOffline, err)
	}
	defer c.Close()
	// Cancelling closes the connection, so a long SEARCH returns.
	stop := context.AfterFunc(ctx, func() { c.Close() })
	defer stop()

	header, text := headerCriteria(words), &imap.SearchCriteria{Text: words}
	byHeader := map[int64][]uint32{}
	for _, f := range folders {
		uids, err := s.searchFolder(ctx, c, f, header)
		if err != nil {
			return s.searchErr(ctx, f, err)
		}
		byHeader[f.ID] = uids
		if len(uids) > 0 {
			found(Found{FolderID: f.ID, UIDs: uids})
		}
	}
	for _, f := range folders {
		uids, err := s.searchFolder(ctx, c, f, text)
		if err != nil {
			return s.searchErr(ctx, f, err)
		}
		uids = slices.DeleteFunc(uids, func(u uint32) bool { return slices.Contains(byHeader[f.ID], u) })
		if len(uids) > 0 {
			found(Found{FolderID: f.ID, UIDs: uids, InBody: true})
		}
	}
	return nil
}

func (s *Syncer) searchErr(ctx context.Context, f cache.Folder, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return fmt.Errorf("searching %s: %w", f.Name, err)
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
	crit *imap.SearchCriteria) ([]uint32, error) {
	sel, err := selectFolder(c, f.Name, true, false)
	if err != nil {
		return nil, err
	}
	if sel.NumMessages == 0 {
		return nil, nil
	}
	var data *imap.SearchData
	err = doWithin(c, "search "+f.Name, searchTimeout, func() (err error) {
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
