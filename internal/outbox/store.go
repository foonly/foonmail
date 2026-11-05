package outbox

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// State is where a message is in the outbox.
type State string

const (
	// Waiting: in the undo window, or waiting to retry after a temporary error.
	Waiting State = "waiting"
	// Sending: handed to the SMTP server. If the daemon dies here, nobody
	// knows whether the server accepted it; see Uncertain.
	Sending State = "sending"
	// Sent: accepted by the SMTP server, not yet copied to Sent.
	Sent State = "sent"
	// Done: sent, and copied to Sent if that is wanted.
	Done State = "done"
	// Uncertain: the daemon stopped, or the connection broke, after the
	// message was handed over. It is never resent without the user's say.
	Uncertain State = "uncertain"
	// Failed: the server refused it for good, or retries ran out.
	Failed State = "failed"
	// Cancelled: undone in time, and moved to Drafts.
	Cancelled State = "cancelled"
)

// Item is one message in the outbox.
type Item struct {
	ID        int64
	Account   string
	State     State
	SendAfter time.Time // next attempt (send or copy to Sent)
	From      string
	Rcpts     []string
	Wire      []byte
	Stored    []byte
	MessageID string
	Subject   string
	To        string
	Attempts  int
	Error     string
	Created   time.Time
}

var migrations = []string{`
CREATE TABLE outbox (
	id         INTEGER PRIMARY KEY,
	account    TEXT NOT NULL,
	state      TEXT NOT NULL,
	send_after INTEGER NOT NULL, -- unix milliseconds
	mail_from  TEXT NOT NULL,
	rcpts      TEXT NOT NULL,    -- newline-separated
	wire       BLOB NOT NULL,
	stored     BLOB NOT NULL,
	message_id TEXT NOT NULL DEFAULT '',
	subject    TEXT NOT NULL DEFAULT '',
	summary    TEXT NOT NULL DEFAULT '',
	attempts   INTEGER NOT NULL DEFAULT 0,
	last_error TEXT NOT NULL DEFAULT '',
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL
);
CREATE INDEX outbox_by_state ON outbox (state, send_after);

-- The largest message each account's SMTP server accepts, from its SIZE
-- extension, so too-large mail is refused before the undo window.
CREATE TABLE size_limits (
	account TEXT PRIMARY KEY,
	bytes   INTEGER NOT NULL
);
`}

// store is the outbox database. Every state change is a single
// conditional UPDATE, so the sender and an undo can't both win.
type store struct {
	db *sql.DB
}

func openStore(path string) (*store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path+"?_txlock=immediate&_pragma=journal_mode(WAL)&_pragma=synchronous(FULL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		db.Close()
		return nil, err
	}
	if version > len(migrations) {
		db.Close()
		return nil, fmt.Errorf("outbox %s is newer than this foonmail", path)
	}
	for v := version; v < len(migrations); v++ {
		if _, err := db.Exec(migrations[v]); err != nil {
			db.Close()
			return nil, fmt.Errorf("outbox migration %d: %w", v+1, err)
		}
		if _, err := db.Exec(fmt.Sprintf("PRAGMA user_version = %d", v+1)); err != nil {
			db.Close()
			return nil, err
		}
	}
	return &store{db: db}, nil
}

func (s *store) close() error { return s.db.Close() }

func ms(t time.Time) int64 { return t.UnixMilli() }

func (s *store) add(ctx context.Context, account string, p *prepared, sendAfter, now time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO outbox (account, state, send_after, mail_from, rcpts, wire, stored, message_id, subject, summary, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		account, Waiting, ms(sendAfter), p.from, strings.Join(p.rcpts, "\n"), p.wire, p.stored,
		p.messageID, p.subject, p.to, ms(now), ms(now))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// move changes an item's state if it is currently in one of from. It
// reports whether it did.
func (s *store) move(ctx context.Context, id int64, to State, from ...State) (bool, error) {
	q := `UPDATE outbox SET state = ?, updated_at = ? WHERE id = ? AND state IN (` +
		strings.TrimSuffix(strings.Repeat("?,", len(from)), ",") + `)`
	args := []any{to, ms(time.Now()), id}
	for _, f := range from {
		args = append(args, f)
	}
	res, err := s.db.ExecContext(ctx, q, args...)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// setOutcome records the result of an attempt.
func (s *store) setOutcome(ctx context.Context, id int64, to State, next time.Time, errText string, countAttempt bool) error {
	inc := 0
	if countAttempt {
		inc = 1
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE outbox SET state = ?, send_after = ?, last_error = ?, attempts = attempts + ?, updated_at = ?
		WHERE id = ?`, to, ms(next), errText, inc, ms(time.Now()), id)
	return err
}

const itemColumns = `id, account, state, send_after, mail_from, rcpts, wire, stored, message_id, subject, summary, attempts, last_error, created_at`

func scanItem(row interface{ Scan(...any) error }) (Item, error) {
	var it Item
	var sendAfter, created int64
	var rcpts string
	err := row.Scan(&it.ID, &it.Account, &it.State, &sendAfter, &it.From, &rcpts, &it.Wire, &it.Stored,
		&it.MessageID, &it.Subject, &it.To, &it.Attempts, &it.Error, &created)
	it.SendAfter, it.Created = time.UnixMilli(sendAfter), time.UnixMilli(created)
	if rcpts != "" {
		it.Rcpts = strings.Split(rcpts, "\n")
	}
	return it, err
}

func (s *store) get(ctx context.Context, id int64) (Item, error) {
	return scanItem(s.db.QueryRowContext(ctx, `SELECT `+itemColumns+` FROM outbox WHERE id = ?`, id))
}

func (s *store) query(ctx context.Context, where string, args ...any) ([]Item, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+itemColumns+` FROM outbox `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Item
	for rows.Next() {
		it, err := scanItem(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// due returns items whose next attempt is now: waiting ones to send, sent
// ones to copy to Sent.
func (s *store) due(ctx context.Context, now time.Time) ([]Item, error) {
	return s.query(ctx, `WHERE state IN (?, ?) AND send_after <= ? ORDER BY send_after, id`, Waiting, Sent, ms(now))
}

// nextDue is when the next waiting or sent item is due, if any.
func (s *store) nextDue(ctx context.Context) (time.Time, bool, error) {
	var next sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT min(send_after) FROM outbox WHERE state IN (?, ?)`, Waiting, Sent).Scan(&next)
	if err != nil || !next.Valid {
		return time.Time{}, false, err
	}
	return time.UnixMilli(next.Int64), true, nil
}

// recover runs at start: a message left in Sending may or may not have
// reached the server.
func (s *store) recover(ctx context.Context) ([]Item, error) {
	stuck, err := s.query(ctx, `WHERE state = ?`, Sending)
	if err != nil {
		return nil, err
	}
	for _, it := range stuck {
		if err := s.setOutcome(ctx, it.ID, Uncertain, it.SendAfter,
			"foonmaild stopped while sending; it may or may not have been sent", false); err != nil {
			return nil, err
		}
	}
	return stuck, nil
}

// list returns everything not done or cancelled, plus the last few that
// are, newest first.
func (s *store) list(ctx context.Context) ([]Item, error) {
	return s.query(ctx, `WHERE state NOT IN (?, ?) OR id IN (
		SELECT id FROM outbox WHERE state IN (?, ?) ORDER BY updated_at DESC LIMIT 20)
		ORDER BY created_at DESC, id DESC`, Done, Cancelled, Done, Cancelled)
}

// prune deletes finished items older than keep.
func (s *store) prune(ctx context.Context, keep time.Duration) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM outbox WHERE state IN (?, ?) AND updated_at < ?`,
		Done, Cancelled, ms(time.Now().Add(-keep)))
	return err
}

func (s *store) sizeLimit(ctx context.Context, account string) (int, bool) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT bytes FROM size_limits WHERE account = ?`, account).Scan(&n)
	return n, err == nil && n > 0
}

func (s *store) setSizeLimit(ctx context.Context, account string, n int) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO size_limits (account, bytes) VALUES (?, ?)
		ON CONFLICT (account) DO UPDATE SET bytes = excluded.bytes`, account, n)
	return err
}

var errNoItem = errors.New("no such outbox item")

func notFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return errNoItem
	}
	return err
}
