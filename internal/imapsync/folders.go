package imapsync

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"

	"foonmail/internal/cache"
	"foonmail/internal/config"
)

var specialUseAttrs = []imap.MailboxAttr{
	imap.MailboxAttrSent, imap.MailboxAttrDrafts, imap.MailboxAttrTrash, imap.MailboxAttrJunk,
	imap.MailboxAttrArchive, imap.MailboxAttrAll, imap.MailboxAttrFlagged,
}

// folderSession keeps the folder list current, syncs watched folders
// whenever STATUS shows they changed, and serves requests from the GUI
// between polls.
func (s *Syncer) folderSession(ctx context.Context, wake <-chan struct{}) error {
	c, err := s.connect(nil)
	if err != nil {
		return err
	}
	defer c.Close()
	condstore := c.Caps().Has(imap.CapCondStore)

	poll := time.NewTimer(0)
	defer poll.Stop()
	for {
		select {
		case <-poll.C:
			folders, err := s.refreshFolders(ctx, c)
			if err != nil {
				return err
			}
			for _, f := range s.resolveWatched(folders) {
				if err := s.pollFolder(ctx, c, f, condstore); err != nil {
					return err
				}
			}
			poll.Reset(s.acct.Folders.PollInterval)
		case req := <-s.requests:
			res := s.handleRequest(ctx, c, req, condstore)
			req.reply <- res
			select {
			case <-c.Closed():
				// The request failed because the connection did.
				return fmt.Errorf("connection lost: %w", res.err)
			default:
			}
		case <-wake:
			return errWoken
		case <-c.Closed():
			return errors.New("connection closed by server")
		case <-ctx.Done():
			_ = do(c, "logout", func() error { return c.Logout().Wait() })
			return ctx.Err()
		}
	}
}

// refreshFolders mirrors the server's folder list into the cache, with
// delimiters and special-use flags, and drops folders that are gone.
func (s *Syncer) refreshFolders(ctx context.Context, c *imapclient.Client) ([]cache.Folder, error) {
	opts := &imap.ListOptions{}
	if c.Caps().Has(imap.CapSpecialUse) {
		opts.ReturnSpecialUse = true
	}
	var boxes []*imap.ListData
	if err := do(c, "list", func() (err error) {
		boxes, err = c.List("", "*", opts).Collect()
		return err
	}); err != nil {
		return nil, err
	}

	onServer := map[string]bool{}
	for _, b := range boxes {
		if slices.Contains(b.Attrs, imap.MailboxAttrNoSelect) || slices.Contains(b.Attrs, imap.MailboxAttrNonExistent) {
			continue
		}
		name := b.Mailbox
		if strings.EqualFold(name, "INBOX") {
			name = "INBOX"
		}
		onServer[name] = true
		f, err := s.db.EnsureFolder(ctx, s.accountID, name)
		if err != nil {
			return nil, err
		}
		special := ""
		for _, a := range b.Attrs {
			if slices.Contains(specialUseAttrs, a) {
				special = string(a)
				break
			}
		}
		delim := ""
		if b.Delim != 0 {
			delim = string(b.Delim)
		}
		if f.Delimiter != delim || f.SpecialUse != special {
			if err := s.db.SetFolderInfo(ctx, f.ID, delim, special); err != nil {
				return nil, err
			}
		}
	}

	cached, err := s.db.Folders(ctx, s.accountID)
	if err != nil {
		return nil, err
	}
	var out []cache.Folder
	for _, f := range cached {
		if !onServer[f.Name] {
			s.log.Info("folder gone from server", "folder", f.Name)
			if err := s.db.DeleteFolder(ctx, f.ID); err != nil {
				return nil, err
			}
			continue
		}
		out = append(out, f)
	}
	return out, nil
}

// resolveWatched maps the configured watched names ("Sent", "@sent") to
// folders. Names that match nothing are logged and skipped.
func (s *Syncer) resolveWatched(folders []cache.Folder) []cache.Folder {
	var out []cache.Folder
	for _, want := range s.acct.Folders.Watched {
		var match *cache.Folder
		for i := range folders {
			f := &folders[i]
			if special, ok := config.SpecialUse[want]; ok && f.SpecialUse == special ||
				!strings.HasPrefix(want, "@") && f.Name == want {
				match = f
				break
			}
		}
		if match == nil {
			s.log.Warn("watched folder not found on server", "folder", want)
			continue
		}
		if match.Name == "INBOX" || slices.ContainsFunc(out, func(f cache.Folder) bool { return f.ID == match.ID }) {
			continue
		}
		out = append(out, *match)
	}
	return out
}

// pollFolder asks STATUS whether a folder changed and syncs it if so.
func (s *Syncer) pollFolder(ctx context.Context, c *imapclient.Client, f cache.Folder, condstore bool) error {
	opts := &imap.StatusOptions{NumMessages: true, UIDNext: true, UIDValidity: true, HighestModSeq: condstore}
	var st *imap.StatusData
	if err := do(c, "status "+f.Name, func() (err error) {
		st, err = c.Status(f.Name, opts).Wait()
		return err
	}); err != nil {
		return err
	}
	known, err := s.db.UIDs(ctx, f.ID)
	if err != nil {
		return err
	}
	changed := f.UIDValidity == 0 ||
		st.UIDValidity != f.UIDValidity ||
		uint32(st.UIDNext) != f.UIDNext ||
		(st.NumMessages != nil && int(*st.NumMessages) != len(known)) ||
		(condstore && st.HighestModSeq != f.HighestModSeq)
	if !changed {
		return nil
	}

	start := time.Now()
	sel, err := selectFolder(c, f.Name, true, condstore)
	if err != nil {
		return err
	}
	res, err := s.syncSelected(ctx, c, f, sel, condstore)
	if err != nil {
		return err
	}
	if err := do(c, "unselect", func() error { return c.Unselect().Wait() }); err != nil {
		return err
	}
	s.log.Info("folder synced", "folder", f.Name, "messages", len(res.uids), "new", len(res.added),
		"gone", len(res.removed), "flag_changes", res.flagChanges, "ms", since(start), "method", res.method)
	s.notifyFolder(f.ID, res)
	return nil
}
