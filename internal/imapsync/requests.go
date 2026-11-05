package imapsync

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"

	"foonmail/internal/cache"
	"foonmail/internal/render"
)

var (
	// ErrOffline means the account's folder connection is down.
	ErrOffline = errors.New("account is offline")
	// ErrNotFound means the folder or message doesn't exist (any more).
	ErrNotFound = errors.New("not found")
)

// FolderChanged reports that the cache's copy of a folder changed, so a
// list showing it should reload.
type FolderChanged struct {
	Account  string
	FolderID int64
}

func (FolderChanged) isEvent() {}

type reqKind int

const (
	reqSync reqKind = iota
	reqFetch
	reqFlags
	reqAppend
	reqDelete
)

// request is work for the folder connection, from the GUI by way of the
// daemon's D-Bus service.
type request struct {
	kind        reqKind
	folderID    int64
	uid         uint32
	uids        []uint32
	add, remove []string
	specialUse  string // reqAppend: the folder, by special-use flag
	raw         []byte
	reply       chan reqResult
}

type reqResult struct {
	messageID int64
	err       error
}

// AccountID is the cache id of the Syncer's account.
func (s *Syncer) AccountID() int64 { return s.accountID }

// SyncFolder syncs a folder now, such as one the GUI just opened.
func (s *Syncer) SyncFolder(ctx context.Context, folderID int64) error {
	return s.request(ctx, request{kind: reqSync, folderID: folderID}).err
}

// FetchBody makes sure a message's raw content is in the cache and returns
// its message id. A message already fetched is answered from the cache,
// so this works offline for anything read before.
func (s *Syncer) FetchBody(ctx context.Context, folderID int64, uid uint32) (int64, error) {
	if id, err := s.db.Location(ctx, folderID, uid); err == nil {
		if raw, err := s.db.Raw(ctx, id); err == nil && raw != nil {
			return id, nil
		}
	}
	r := s.request(ctx, request{kind: reqFetch, folderID: folderID, uid: uid})
	return r.messageID, r.err
}

// SetFlags adds and removes flags on messages, on the server and in the
// cache, such as \Seen when a message is read.
func (s *Syncer) SetFlags(ctx context.Context, folderID int64, uids []uint32, add, remove []string) error {
	if len(uids) == 0 || (len(add) == 0 && len(remove) == 0) {
		return nil
	}
	return s.request(ctx, request{kind: reqFlags, folderID: folderID, uids: uids, add: add, remove: remove}).err
}

func (s *Syncer) request(ctx context.Context, req request) reqResult {
	req.reply = make(chan reqResult, 1)
	select {
	case s.requests <- req:
	case <-ctx.Done():
		return reqResult{err: ctx.Err()}
	}
	select {
	case r := <-req.reply:
		return r
	case <-ctx.Done():
		return reqResult{err: ctx.Err()}
	}
}

// handleRequest runs a request on the folder connection. It leaves no
// folder selected.
func (s *Syncer) handleRequest(ctx context.Context, c *imapclient.Client, req request, condstore bool) reqResult {
	if req.kind == reqAppend {
		return reqResult{err: s.appendMessage(ctx, c, req, condstore)}
	}
	f, err := s.db.FolderByID(ctx, req.folderID)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && f.AccountID != s.accountID) {
		return reqResult{err: fmt.Errorf("folder %d: %w", req.folderID, ErrNotFound)}
	}
	if err != nil {
		return reqResult{err: err}
	}
	readOnly := req.kind != reqFlags && req.kind != reqDelete
	sel, err := selectFolder(c, f.Name, readOnly, condstore)
	if err != nil {
		return reqResult{err: err}
	}
	defer do(c, "unselect", func() error { return c.Unselect().Wait() })

	switch req.kind {
	case reqSync:
		res, err := s.syncSelected(ctx, c, f, sel, condstore)
		if err != nil {
			return reqResult{err: err}
		}
		s.log.Info("folder synced on request", "folder", f.Name, "messages", len(res.uids), "new", len(res.added),
			"gone", len(res.removed), "flag_changes", res.flagChanges)
		s.notifyFolder(f.ID, res)
		return reqResult{}

	case reqFetch:
		id, err := s.fetchBody(ctx, c, f, sel, req.uid, condstore)
		return reqResult{messageID: id, err: err}

	case reqDelete:
		err := s.deleteMessages(ctx, c, f, req.uids)
		if err == nil {
			s.notify(FolderChanged{Account: s.acct.Name, FolderID: f.ID})
		}
		return reqResult{err: err}

	case reqFlags:
		err := s.storeFlags(ctx, c, f, req)
		if err == nil {
			s.notify(FolderChanged{Account: s.acct.Name, FolderID: f.ID})
		}
		return reqResult{err: err}
	}
	return reqResult{err: errors.New("unknown request")}
}

func (s *Syncer) fetchBody(ctx context.Context, c *imapclient.Client, f cache.Folder, sel *imap.SelectData,
	uid uint32, condstore bool) (int64, error) {
	// The message may be newer than the cache's copy of the folder.
	id, err := s.db.Location(ctx, f.ID, uid)
	if errors.Is(err, sql.ErrNoRows) {
		res, serr := s.syncSelected(ctx, c, f, sel, condstore)
		if serr != nil {
			return 0, serr
		}
		s.notifyFolder(f.ID, res)
		id, err = s.db.Location(ctx, f.ID, uid)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("%s uid %d: %w", f.Name, uid, ErrNotFound)
	}
	if err != nil {
		return 0, err
	}

	section := &imap.FetchItemBodySection{Peek: true} // Peek: reading isn't the same as opening
	var bufs []*imapclient.FetchMessageBuffer
	if err := do(c, "fetch body", func() (err error) {
		bufs, err = c.Fetch(imap.UIDSetNum(imap.UID(uid)),
			&imap.FetchOptions{UID: true, BodySection: []*imap.FetchItemBodySection{section}}).Collect()
		return err
	}); err != nil {
		return 0, err
	}
	var raw []byte
	for _, b := range bufs {
		if uint32(b.UID) == uid {
			raw = b.FindBodySection(section)
		}
	}
	if raw == nil {
		return 0, fmt.Errorf("%s uid %d: the server returned no body: %w", f.Name, uid, ErrNotFound)
	}
	// Index what the reader will show, for body search.
	var text string
	if m, err := render.Render(raw, render.Options{}); err == nil {
		text = m.Text
	}
	if err := s.db.SetRaw(ctx, id, raw, text); err != nil {
		return 0, err
	}
	return id, nil
}

func (s *Syncer) storeFlags(ctx context.Context, c *imapclient.Client, f cache.Folder, req request) error {
	set := imap.UIDSetNum(toUIDs(req.uids)...)
	var updates []cache.FlagUpdate
	for _, op := range []struct {
		kind  imap.StoreFlagsOp
		flags []string
	}{{imap.StoreFlagsAdd, req.add}, {imap.StoreFlagsDel, req.remove}} {
		if len(op.flags) == 0 {
			continue
		}
		flags := make([]imap.Flag, len(op.flags))
		for i, fl := range op.flags {
			flags[i] = imap.Flag(fl)
		}
		var bufs []*imapclient.FetchMessageBuffer
		if err := do(c, "store flags", func() (err error) {
			bufs, err = c.Store(set, &imap.StoreFlags{Op: op.kind, Flags: flags}, nil).Collect()
			return err
		}); err != nil {
			return err
		}
		for _, b := range bufs {
			if b.UID != 0 && b.Flags != nil {
				updates = append(updates, cache.FlagUpdate{UID: uint32(b.UID), Flags: flagStrings(b.Flags), ModSeq: b.ModSeq})
			}
		}
	}
	return s.db.SetFlags(ctx, f.ID, updates)
}

// notifyFolder reports a folder change if a sync changed anything.
func (s *Syncer) notifyFolder(folderID int64, res syncResult) {
	if len(res.added) > 0 || len(res.removed) > 0 || res.flagChanges > 0 {
		s.notify(FolderChanged{Account: s.acct.Name, FolderID: folderID})
	}
}

// AppendSent stores a sent message in the account's \Sent folder, marked read.
func (s *Syncer) AppendSent(ctx context.Context, raw []byte) error {
	return s.request(ctx, request{kind: reqAppend, specialUse: `\Sent`, raw: raw, add: []string{`\Seen`}}).err
}

// AppendDraft stores a message in the account's \Drafts folder.
func (s *Syncer) AppendDraft(ctx context.Context, raw []byte) error {
	return s.request(ctx, request{kind: reqAppend, specialUse: `\Drafts`, raw: raw, add: []string{`\Draft`, `\Seen`}}).err
}

// appendMessage APPENDs to the folder with the request's special-use flag,
// then syncs that folder so the message shows up in the cache.
func (s *Syncer) appendMessage(ctx context.Context, c *imapclient.Client, req request, condstore bool) error {
	folders, err := s.db.Folders(ctx, s.accountID)
	if err != nil {
		return err
	}
	var f *cache.Folder
	for i := range folders {
		if folders[i].SpecialUse == req.specialUse {
			f = &folders[i]
			break
		}
	}
	if f == nil {
		return fmt.Errorf("no folder is flagged %s: %w", req.specialUse, ErrNotFound)
	}
	flags := make([]imap.Flag, len(req.add))
	for i, fl := range req.add {
		flags[i] = imap.Flag(fl)
	}
	if err := do(c, "append to "+f.Name, func() error {
		cmd := c.Append(f.Name, int64(len(req.raw)), &imap.AppendOptions{Flags: flags})
		if _, err := cmd.Write(req.raw); err != nil {
			cmd.Close()
			return err
		}
		if err := cmd.Close(); err != nil {
			return err
		}
		_, err := cmd.Wait()
		return err
	}); err != nil {
		return err
	}
	s.log.Info("appended", "folder", f.Name, "bytes", len(req.raw))

	sel, err := selectFolder(c, f.Name, true, condstore)
	if err != nil {
		return err
	}
	defer do(c, "unselect", func() error { return c.Unselect().Wait() })
	res, err := s.syncSelected(ctx, c, *f, sel, condstore)
	if err != nil {
		return err
	}
	s.notifyFolder(f.ID, res)
	return nil
}

// DeleteMessages removes messages from a folder for good, such as a draft
// that was sent or saved again.
func (s *Syncer) DeleteMessages(ctx context.Context, folderID int64, uids []uint32) error {
	if len(uids) == 0 {
		return nil
	}
	return s.request(ctx, request{kind: reqDelete, folderID: folderID, uids: uids}).err
}

// deleteMessages marks messages \Deleted and expunges exactly those with UID
// EXPUNGE. Without UIDPLUS a plain EXPUNGE would also remove anything else
// marked \Deleted in the folder, so then they are only marked.
func (s *Syncer) deleteMessages(ctx context.Context, c *imapclient.Client, f cache.Folder, uids []uint32) error {
	set := imap.UIDSetNum(toUIDs(uids)...)
	if err := do(c, "mark deleted", func() error {
		return c.Store(set, &imap.StoreFlags{Op: imap.StoreFlagsAdd, Flags: []imap.Flag{imap.FlagDeleted}, Silent: true}, nil).Close()
	}); err != nil {
		return err
	}
	caps := c.Caps()
	if !caps.Has(imap.CapUIDPlus) && !caps.Has(imap.CapIMAP4rev2) {
		s.log.Warn("no UIDPLUS: messages marked deleted but not expunged", "folder", f.Name, "uids", uids)
		return nil // the next sync records the flag
	}
	if err := do(c, "expunge", func() error { return c.UIDExpunge(set).Close() }); err != nil {
		return err
	}
	return s.db.RemoveUIDs(ctx, f.ID, uids)
}
