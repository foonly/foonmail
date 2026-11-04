// Package cache is foonmail's local mail store: one SQLite database holding
// accounts, folders with their sync state, messages (with the raw message
// once fetched) and a full-text index.
//
// The daemon is the only writer. A message is stored once per account and
// linked to every folder it is in through locations, so a copy in INBOX
// and Archive, or a Gmail message with several labels, is one message.
package cache

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" driver
)

// DB is an open cache.
type DB struct {
	db *sql.DB
}

// Open opens or creates the cache at path and brings its schema up to date.
func Open(path string) (*DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	// Immediate transactions take the write lock up front, so two writers
	// wait on busy_timeout instead of failing on lock upgrade.
	dsn := path + "?_txlock=immediate" +
		"&_pragma=foreign_keys(1)" +
		"&_pragma=journal_mode(WAL)" +
		"&_pragma=synchronous(NORMAL)" +
		"&_pragma=busy_timeout(5000)"
	sqldb, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	d := &DB{db: sqldb}
	if err := d.migrate(); err != nil {
		sqldb.Close()
		return nil, fmt.Errorf("cache %s: %w", path, err)
	}
	return d, nil
}

// Close closes the database.
func (d *DB) Close() error { return d.db.Close() }

func (d *DB) migrate() error {
	var version int
	if err := d.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version > len(migrations) {
		return fmt.Errorf("schema version %d is newer than this foonmail (%d); upgrade foonmail", version, len(migrations))
	}
	for v := version; v < len(migrations); v++ {
		err := d.tx(context.Background(), func(tx *sql.Tx) error {
			if _, err := tx.Exec(migrations[v]); err != nil {
				return fmt.Errorf("migration %d: %w", v+1, err)
			}
			_, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", v+1))
			return err
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func (d *DB) tx(ctx context.Context, f func(*sql.Tx) error) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := f(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

// Accounts and folders

// EnsureAccount returns the id of the named account, creating it if needed.
func (d *DB) EnsureAccount(ctx context.Context, name string) (int64, error) {
	if _, err := d.db.ExecContext(ctx,
		`INSERT INTO accounts (name) VALUES (?) ON CONFLICT (name) DO NOTHING`, name); err != nil {
		return 0, err
	}
	var id int64
	err := d.db.QueryRowContext(ctx, `SELECT id FROM accounts WHERE name = ?`, name).Scan(&id)
	return id, err
}

// Folder is a mailbox and how far it has been synced.
type Folder struct {
	ID            int64
	AccountID     int64
	Name          string
	Delimiter     string
	SpecialUse    string // e.g. `\Sent`; empty when none
	UIDValidity   uint32 // 0 when never synced
	HighestModSeq uint64
	UIDNext       uint32
	SyncedAt      time.Time
}

const folderColumns = `id, account_id, name, delimiter, special_use, uid_validity, highest_modseq, uid_next, synced_at`

func scanFolder(row interface{ Scan(...any) error }) (Folder, error) {
	var f Folder
	var syncedAt int64
	err := row.Scan(&f.ID, &f.AccountID, &f.Name, &f.Delimiter, &f.SpecialUse,
		&f.UIDValidity, &f.HighestModSeq, &f.UIDNext, &syncedAt)
	if syncedAt != 0 {
		f.SyncedAt = time.Unix(syncedAt, 0)
	}
	return f, err
}

// EnsureFolder returns the named folder, creating it if needed.
func (d *DB) EnsureFolder(ctx context.Context, accountID int64, name string) (Folder, error) {
	if _, err := d.db.ExecContext(ctx,
		`INSERT INTO folders (account_id, name) VALUES (?, ?) ON CONFLICT (account_id, name) DO NOTHING`,
		accountID, name); err != nil {
		return Folder{}, err
	}
	return scanFolder(d.db.QueryRowContext(ctx,
		`SELECT `+folderColumns+` FROM folders WHERE account_id = ? AND name = ?`, accountID, name))
}

// Folders lists an account's folders by name.
func (d *DB) Folders(ctx context.Context, accountID int64) ([]Folder, error) {
	rows, err := d.db.QueryContext(ctx,
		`SELECT `+folderColumns+` FROM folders WHERE account_id = ? ORDER BY name`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Folder
	for rows.Next() {
		f, err := scanFolder(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// DeleteFolder forgets a folder that no longer exists on the server.
// Messages left in no folder are deleted.
func (d *DB) DeleteFolder(ctx context.Context, folderID int64) error {
	return d.tx(ctx, func(tx *sql.Tx) error {
		ids, err := queryIDs(ctx, tx, `SELECT DISTINCT message_id FROM locations WHERE folder_id = ?`, folderID)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM folders WHERE id = ?`, folderID); err != nil {
			return err
		}
		return deleteOrphans(ctx, tx, ids)
	})
}

// SetFolderInfo records the hierarchy delimiter and special-use attribute
// from LIST.
func (d *DB) SetFolderInfo(ctx context.Context, folderID int64, delimiter, specialUse string) error {
	_, err := d.db.ExecContext(ctx,
		`UPDATE folders SET delimiter = ?, special_use = ? WHERE id = ?`, delimiter, specialUse, folderID)
	return err
}

// SaveSyncState records how far a folder has been synced.
func (d *DB) SaveSyncState(ctx context.Context, folderID int64, uidValidity uint32, highestModSeq uint64, uidNext uint32) error {
	_, err := d.db.ExecContext(ctx,
		`UPDATE folders SET uid_validity = ?, highest_modseq = ?, uid_next = ?, synced_at = ? WHERE id = ?`,
		uidValidity, highestModSeq, uidNext, time.Now().Unix(), folderID)
	return err
}

// ResetFolder forgets every message location in a folder, for when its
// UIDVALIDITY changed and all UIDs are meaningless. Messages left in no
// folder are deleted.
func (d *DB) ResetFolder(ctx context.Context, folderID int64, uidValidity uint32) error {
	return d.tx(ctx, func(tx *sql.Tx) error {
		ids, err := queryIDs(ctx, tx, `SELECT DISTINCT message_id FROM locations WHERE folder_id = ?`, folderID)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM locations WHERE folder_id = ?`, folderID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE folders SET uid_validity = ?, highest_modseq = 0, uid_next = 0 WHERE id = ?`,
			uidValidity, folderID); err != nil {
			return err
		}
		return deleteOrphans(ctx, tx, ids)
	})
}

// Messages

// Address is a mail address with an optional display name.
type Address struct {
	Name string
	Addr string
}

func (a Address) String() string {
	if a.Name == "" {
		return a.Addr
	}
	return fmt.Sprintf("%s <%s>", a.Name, a.Addr)
}

// Envelope is the header information stored for every message.
type Envelope struct {
	MessageID  string // without <>
	Subject    string
	From       Address
	To, Cc     []Address
	Date       time.Time
	InReplyTo  string   // without <>
	References []string // without <>
}

// NewMessage is a message found in a folder during sync.
type NewMessage struct {
	UID      uint32
	Flags    []string
	ModSeq   uint64
	Size     int64
	Envelope Envelope
}

// AddMessages stores messages found in a folder. A message whose
// Message-ID and size match one already stored for the account is linked
// to it instead of stored again. Adding a UID that is already there
// updates its flags.
func (d *DB) AddMessages(ctx context.Context, folderID int64, msgs []NewMessage) error {
	return d.tx(ctx, func(tx *sql.Tx) error {
		var accountID int64
		if err := tx.QueryRowContext(ctx, `SELECT account_id FROM folders WHERE id = ?`, folderID).Scan(&accountID); err != nil {
			return fmt.Errorf("folder %d: %w", folderID, err)
		}
		for _, m := range msgs {
			id, err := findOrInsertMessage(ctx, tx, accountID, m)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO locations (folder_id, uid, message_id, flags, modseq) VALUES (?, ?, ?, ?, ?)
				ON CONFLICT (folder_id, uid) DO UPDATE SET flags = excluded.flags, modseq = excluded.modseq`,
				folderID, m.UID, id, joinFlags(m.Flags), m.ModSeq); err != nil {
				return err
			}
		}
		return nil
	})
}

func findOrInsertMessage(ctx context.Context, tx *sql.Tx, accountID int64, m NewMessage) (int64, error) {
	e := m.Envelope
	if e.MessageID != "" {
		var id int64
		err := tx.QueryRowContext(ctx,
			`SELECT id FROM messages WHERE account_id = ? AND message_id = ? AND size = ? LIMIT 1`,
			accountID, e.MessageID, m.Size).Scan(&id)
		if err == nil {
			return id, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return 0, err
		}
	}
	var date int64
	if !e.Date.IsZero() {
		date = e.Date.Unix()
	}
	res, err := tx.ExecContext(ctx, `
		INSERT INTO messages (account_id, message_id, subject, from_name, from_addr, to_list, cc_list,
		                      date, in_reply_to, refs, size)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		accountID, e.MessageID, e.Subject, e.From.Name, e.From.Addr, joinAddrs(e.To), joinAddrs(e.Cc),
		date, e.InReplyTo, strings.Join(e.References, " "), m.Size)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// FlagUpdate is a flag change seen for one UID.
type FlagUpdate struct {
	UID    uint32
	Flags  []string
	ModSeq uint64
}

// SetFlags updates flags. Unknown UIDs are ignored: a message must be added
// before its flags can change.
func (d *DB) SetFlags(ctx context.Context, folderID int64, updates []FlagUpdate) error {
	return d.tx(ctx, func(tx *sql.Tx) error {
		for _, u := range updates {
			if _, err := tx.ExecContext(ctx,
				`UPDATE locations SET flags = ?, modseq = max(modseq, ?) WHERE folder_id = ? AND uid = ?`,
				joinFlags(u.Flags), u.ModSeq, folderID, u.UID); err != nil {
				return err
			}
		}
		return nil
	})
}

// RemoveUIDs forgets messages expunged from a folder. Messages left in no
// folder are deleted along with their raw content and search entry.
func (d *DB) RemoveUIDs(ctx context.Context, folderID int64, uids []uint32) error {
	return d.tx(ctx, func(tx *sql.Tx) error {
		var ids []int64
		for chunk := range slices.Chunk(uids, 500) {
			in, args := inList(folderID, chunk)
			got, err := queryIDs(ctx, tx,
				`SELECT DISTINCT message_id FROM locations WHERE folder_id = ? AND uid IN (`+in+`)`, args...)
			if err != nil {
				return err
			}
			ids = append(ids, got...)
			if _, err := tx.ExecContext(ctx,
				`DELETE FROM locations WHERE folder_id = ? AND uid IN (`+in+`)`, args...); err != nil {
				return err
			}
		}
		return deleteOrphans(ctx, tx, ids)
	})
}

// UIDs returns a folder's UIDs in ascending order.
func (d *DB) UIDs(ctx context.Context, folderID int64) ([]uint32, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT uid FROM locations WHERE folder_id = ? ORDER BY uid`, folderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []uint32
	for rows.Next() {
		var uid uint32
		if err := rows.Scan(&uid); err != nil {
			return nil, err
		}
		out = append(out, uid)
	}
	return out, rows.Err()
}

// Flags returns the flags of every message in a folder by UID.
func (d *DB) Flags(ctx context.Context, folderID int64) (map[uint32][]string, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT uid, flags FROM locations WHERE folder_id = ?`, folderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[uint32][]string{}
	for rows.Next() {
		var uid uint32
		var flags string
		if err := rows.Scan(&uid, &flags); err != nil {
			return nil, err
		}
		out[uid] = splitFlags(flags)
	}
	return out, rows.Err()
}

// UnreadCount counts messages in a folder without the \Seen flag.
func (d *DB) UnreadCount(ctx context.Context, folderID int64) (int, error) {
	var n int
	err := d.db.QueryRowContext(ctx,
		`SELECT count(*) FROM locations WHERE folder_id = ? AND instr(' ' || flags || ' ', ' \Seen ') = 0`,
		folderID).Scan(&n)
	return n, err
}

// SetRaw stores a fetched raw message and the plain text of its body for
// search. bodyText is what the user sees: the text part, or HTML converted
// to text.
func (d *DB) SetRaw(ctx context.Context, messageID int64, raw []byte, bodyText string) error {
	return d.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`UPDATE messages SET raw = ?, fetched_at = ? WHERE id = ?`, raw, time.Now().Unix(), messageID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return fmt.Errorf("message %d: %w", messageID, sql.ErrNoRows)
		}
		_, err = tx.ExecContext(ctx, `UPDATE message_text SET body = ? WHERE rowid = ?`, bodyText, messageID)
		return err
	})
}

// Raw returns a message's raw content, or nil if it hasn't been fetched.
func (d *DB) Raw(ctx context.Context, messageID int64) ([]byte, error) {
	var raw []byte
	err := d.db.QueryRowContext(ctx, `SELECT raw FROM messages WHERE id = ?`, messageID).Scan(&raw)
	return raw, err
}

// Header is the stored header information of one message.
type Header struct {
	MessageID  string
	Subject    string
	From       Address
	To, Cc     string // formatted lists, as shown
	Date       time.Time
	InReplyTo  string
	References []string
	Size       int64
}

// Header returns a message's stored header information.
func (d *DB) Header(ctx context.Context, messageID int64) (Header, error) {
	var h Header
	var date int64
	var refs string
	err := d.db.QueryRowContext(ctx, `
		SELECT message_id, subject, from_name, from_addr, to_list, cc_list, date, in_reply_to, refs, size
		FROM messages WHERE id = ?`, messageID).Scan(
		&h.MessageID, &h.Subject, &h.From.Name, &h.From.Addr, &h.To, &h.Cc, &date, &h.InReplyTo, &refs, &h.Size)
	if date != 0 {
		h.Date = time.Unix(date, 0)
	}
	if refs != "" {
		h.References = strings.Split(refs, " ")
	}
	return h, err
}

// Listing and search

// Summary is one row of a message list.
type Summary struct {
	ID       int64 // messages.id
	FolderID int64
	UID      uint32
	Subject  string
	From     Address
	Date     time.Time
	Flags    []string
	Fetched  bool // raw content is cached
}

const summarySelect = `
	SELECT m.id, l.folder_id, l.uid, m.subject, m.from_name, m.from_addr, m.date, l.flags, m.raw IS NOT NULL
	FROM locations l JOIN messages m ON m.id = l.message_id`

// List returns a folder's messages, newest first.
func (d *DB) List(ctx context.Context, folderID int64, limit, offset int) ([]Summary, error) {
	return d.summaries(ctx, summarySelect+` WHERE l.folder_id = ? ORDER BY m.date DESC, l.uid DESC LIMIT ? OFFSET ?`,
		folderID, limit, offset)
}

// Search finds messages whose subject, sender or recipients contain words
// starting with each of the given words, newest first. folderID 0 searches
// every folder of the account.
func (d *DB) Search(ctx context.Context, accountID, folderID int64, words []string, limit int) ([]Summary, error) {
	q := headerQuery(words)
	if q == "" {
		return nil, nil
	}
	sqlText := summarySelect + `
		JOIN folders f ON f.id = l.folder_id
		WHERE m.id IN (SELECT rowid FROM message_text WHERE message_text MATCH ?)
		  AND f.account_id = ?`
	args := []any{q, accountID}
	if folderID != 0 {
		sqlText += ` AND l.folder_id = ?`
		args = append(args, folderID)
	}
	sqlText += ` ORDER BY m.date DESC LIMIT ?`
	return d.summaries(ctx, sqlText, append(args, limit)...)
}

// headerQuery builds an FTS5 query matching every word as a prefix in the
// subject or people columns. Each word is quoted, so FTS5 syntax typed by
// the user is searched for literally.
func headerQuery(words []string) string {
	var terms []string
	for _, w := range words {
		if w = strings.TrimSpace(w); w != "" {
			terms = append(terms, `"`+strings.ReplaceAll(w, `"`, `""`)+`"*`)
		}
	}
	if len(terms) == 0 {
		return ""
	}
	return "{subject people} : (" + strings.Join(terms, " AND ") + ")"
}

func (d *DB) summaries(ctx context.Context, query string, args ...any) ([]Summary, error) {
	rows, err := d.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Summary
	for rows.Next() {
		var s Summary
		var date int64
		var flags string
		if err := rows.Scan(&s.ID, &s.FolderID, &s.UID, &s.Subject, &s.From.Name, &s.From.Addr,
			&date, &flags, &s.Fetched); err != nil {
			return nil, err
		}
		if date != 0 {
			s.Date = time.Unix(date, 0)
		}
		s.Flags = splitFlags(flags)
		out = append(out, s)
	}
	return out, rows.Err()
}

// Helpers

func deleteOrphans(ctx context.Context, tx *sql.Tx, ids []int64) error {
	for chunk := range slices.Chunk(ids, 500) {
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(chunk)), ",")
		args := make([]any, len(chunk))
		for i, id := range chunk {
			args[i] = id
		}
		if _, err := tx.ExecContext(ctx, `
			DELETE FROM messages WHERE id IN (`+placeholders+`)
			AND NOT EXISTS (SELECT 1 FROM locations WHERE message_id = messages.id)`, args...); err != nil {
			return err
		}
	}
	return nil
}

func queryIDs(ctx context.Context, tx *sql.Tx, query string, args ...any) ([]int64, error) {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func inList(folderID int64, uids []uint32) (string, []any) {
	args := make([]any, 0, len(uids)+1)
	args = append(args, folderID)
	for _, u := range uids {
		args = append(args, u)
	}
	return strings.TrimSuffix(strings.Repeat("?,", len(uids)), ","), args
}

func joinFlags(flags []string) string {
	f := slices.Clone(flags)
	slices.Sort(f)
	return strings.Join(slices.Compact(f), " ")
}

func splitFlags(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, " ")
}

func joinAddrs(as []Address) string {
	s := make([]string, len(as))
	for i, a := range as {
		s[i] = a.String()
	}
	return strings.Join(s, ", ")
}
