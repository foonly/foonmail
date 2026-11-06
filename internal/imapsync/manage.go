package imapsync

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/emersion/go-imap/v2/imapclient"

	"foonmail/internal/cache"
)

// ErrInvalid means a request can't be done as asked, such as deleting
// INBOX.
var ErrInvalid = errors.New("not allowed")

// CreateFolder makes a folder on the server and returns its cache id.
// The name separates levels with "/" ("Clients/Acme"), whatever the
// server's own separator is.
func (s *Syncer) CreateFolder(ctx context.Context, name string) (int64, error) {
	r := s.request(ctx, request{kind: reqCreateFolder, name: name})
	return r.messageID, r.err
}

// DeleteFolder deletes a folder, and the mail in it, from the server.
// INBOX and the special folders (Sent, Trash…) can't be deleted.
func (s *Syncer) DeleteFolder(ctx context.Context, folderID int64) error {
	return s.request(ctx, request{kind: reqDeleteFolder, folderID: folderID}).err
}

// serverName turns a "/"-separated name into the server's, using the
// separator the account's folders have.
func serverName(name string, folders []cache.Folder) (string, error) {
	parts := strings.Split(name, "/")
	for i, p := range parts {
		parts[i] = strings.TrimSpace(p)
		if parts[i] == "" {
			return "", fmt.Errorf("%q has an empty part: %w", name, ErrInvalid)
		}
	}
	delim := "/"
	for _, f := range folders {
		if f.Delimiter != "" {
			delim = f.Delimiter
			break
		}
	}
	full := strings.Join(parts, delim)
	if strings.EqualFold(parts[0], "INBOX") && len(parts) == 1 {
		return "", fmt.Errorf("INBOX already exists: %w", ErrInvalid)
	}
	for _, f := range folders {
		if strings.EqualFold(f.Name, full) {
			return "", fmt.Errorf("%s already exists: %w", f.Name, ErrInvalid)
		}
	}
	return full, nil
}

func (s *Syncer) createFolder(ctx context.Context, c *imapclient.Client, name string) (int64, error) {
	folders, err := s.db.Folders(ctx, s.accountID)
	if err != nil {
		return 0, err
	}
	full, err := serverName(name, folders)
	if err != nil {
		return 0, err
	}
	if err := do(c, "create "+full, func() error { return c.Create(full, nil).Wait() }); err != nil {
		return 0, err
	}
	// Clients that show only subscribed folders should see it too.
	if err := do(c, "subscribe "+full, func() error { return c.Subscribe(full).Wait() }); err != nil {
		s.log.Warn("subscribing to a new folder", "folder", full, "err", err)
	}
	if _, err := s.refreshFolders(ctx, c); err != nil {
		return 0, err
	}
	f, err := s.db.EnsureFolder(ctx, s.accountID, full)
	if err != nil {
		return 0, err
	}
	s.log.Info("folder created", "folder", full)
	s.notify(FolderChanged{Account: s.acct.Name, FolderID: f.ID})
	return f.ID, nil
}

func (s *Syncer) deleteFolder(ctx context.Context, c *imapclient.Client, folderID int64) error {
	f, err := s.db.FolderByID(ctx, folderID)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && f.AccountID != s.accountID) {
		return fmt.Errorf("folder %d: %w", folderID, ErrNotFound)
	}
	if err != nil {
		return err
	}
	if f.Name == "INBOX" || f.SpecialUse != "" {
		return fmt.Errorf("%s is one of the account's special folders: %w", f.Name, ErrInvalid)
	}
	if err := do(c, "delete "+f.Name, func() error { return c.Delete(f.Name).Wait() }); err != nil {
		return err
	}
	_ = do(c, "unsubscribe "+f.Name, func() error { return c.Unsubscribe(f.Name).Wait() })
	if err := s.db.DeleteFolder(ctx, f.ID); err != nil {
		return err
	}
	s.log.Info("folder deleted", "folder", f.Name)
	s.notify(FolderChanged{Account: s.acct.Name, FolderID: f.ID})
	return nil
}
