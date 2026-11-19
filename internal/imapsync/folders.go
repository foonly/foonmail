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
// whenever STATUS shows they changed, serves requests from the GUI between
// polls, and prefetches the bodies of the newest INBOX messages when it has
// nothing else to do.
func (s *Syncer) folderSession(ctx context.Context, wake <-chan struct{}) error {
	c, err := s.connect(nil)
	if err != nil {
		return err
	}
	defer c.Close()
	condstore := c.Caps().Has(imap.CapCondStore)

	poll := time.NewTimer(0)
	defer poll.Stop()
	prefetching := false // the next idle moment fetches a few more bodies
	for {
		// A waiting request goes first; prefetching is for when the
		// connection would otherwise sit idle. (Anything else waits at
		// most one short prefetch step.)
		var idle <-chan struct{}
		if prefetching {
			idle = alwaysReady
		}
		select {
		case req := <-s.requests:
			if err := s.serveRequest(ctx, c, req, condstore); err != nil {
				return err
			}
			continue
		default:
		}
		select {
		case <-idle:
			more, err := s.prefetchStep(ctx, c, condstore)
			if err := closedErr(c, err); err != nil {
				return err
			}
			if err != nil {
				s.log.Warn("prefetching bodies", "err", err)
			}
			prefetching = more && err == nil
		case <-s.prefetchKick:
			prefetching = true
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
			prefetching = true // also retries what failed last time
		case req := <-s.requests:
			if err := s.serveRequest(ctx, c, req, condstore); err != nil {
				return err
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

const (
	// prefetchCount is how many of INBOX's newest messages have their
	// bodies fetched in the background, so they open at once.
	prefetchCount = 30
	// prefetchMaxSize leaves bigger messages, such as ones with large
	// attachments, to be fetched when opened.
	prefetchMaxSize = 2 << 20
	// prefetchStepCount is how many bodies one prefetch step fetches
	// before the connection checks for requests again.
	prefetchStepCount = 10
)

// prefetchStep fetches the bodies of a few of INBOX's newest messages that
// aren't cached yet, and reports whether more are left.
func (s *Syncer) prefetchStep(ctx context.Context, c *imapclient.Client, condstore bool) (more bool, err error) {
	inbox, err := s.db.EnsureFolder(ctx, s.accountID, "INBOX")
	if err != nil {
		return false, err
	}
	sts, err := s.db.NewestBodyStates(ctx, inbox.ID, prefetchCount)
	if err != nil {
		return false, err
	}
	var uids []uint32
	for _, st := range sts {
		if !st.Fetched && st.Size <= prefetchMaxSize {
			uids = append(uids, st.UID)
		}
	}
	if len(uids) == 0 {
		return false, nil
	}
	step := uids[:min(len(uids), prefetchStepCount)]
	sel, err := selectFolder(c, inbox.Name, true, condstore)
	if err != nil {
		return false, err
	}
	defer do(c, "unselect", func() error { return c.Unselect().Wait() })
	if err := s.fetchBodies(ctx, c, inbox, sel, step, condstore); err != nil {
		return false, err
	}
	s.log.Debug("prefetched bodies", "count", len(step), "left", len(uids)-len(step))
	return len(uids) > len(step), nil
}

// kickPrefetch has the folder connection prefetch bodies when it's next
// idle, such as after new mail.
func (s *Syncer) kickPrefetch() {
	select {
	case s.prefetchKick <- struct{}{}:
	default:
	}
}

// alwaysReady is a closed channel, for a select case that is always ready.
var alwaysReady = func() chan struct{} { c := make(chan struct{}); close(c); return c }()

// serveRequest answers a request. It returns an error only if the
// connection is gone, such as because a command timed out.
func (s *Syncer) serveRequest(ctx context.Context, c *imapclient.Client, req request, condstore bool) error {
	res := s.handleRequest(ctx, c, req, condstore)
	req.reply <- res
	return closedErr(c, res.err)
}

// closedErr reports a connection that closed, with the error that failed
// because of it.
func closedErr(c *imapclient.Client, err error) error {
	select {
	case <-c.Closed():
		return fmt.Errorf("connection lost: %w", err)
	default:
		return nil
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
