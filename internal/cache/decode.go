package cache

import (
	"context"
	"database/sql"
	"log/slog"

	"foonmail/internal/hdr"
)

// decodeNames decodes encoded words in the names of addresses.
func decodeNames(as []Address) []Address {
	out := make([]Address, len(as))
	for i, a := range as {
		out[i] = Address{Name: hdr.Decode(a.Name), Addr: a.Addr}
	}
	return out
}

// decodeStored decodes the encoded words that older versions stored
// as they were, when the charset was one the decoder didn't know
// (windows-1252, ISO-8859-15…). It costs a scan of the messages table,
// so it is done once when the cache is opened for writing; what still
// looks encoded afterwards is malformed and stays as it is.
func (d *DB) decodeStored() {
	if err := d.redecode(context.Background()); err != nil {
		slog.Warn("decoding stored headers", "err", err)
	}
}

func (d *DB) redecode(ctx context.Context) error {
	type row struct {
		id                            int64
		subject, fromName, toList, cc string
	}
	var rows []row
	q, err := d.db.QueryContext(ctx, `
		SELECT id, subject, from_name, to_list, cc_list FROM messages
		WHERE subject LIKE '%=?%' OR from_name LIKE '%=?%' OR to_list LIKE '%=?%' OR cc_list LIKE '%=?%'`)
	if err != nil {
		return err
	}
	for q.Next() {
		var r row
		if err := q.Scan(&r.id, &r.subject, &r.fromName, &r.toList, &r.cc); err != nil {
			q.Close()
			return err
		}
		rows = append(rows, r)
	}
	q.Close()
	if err := q.Err(); err != nil {
		return err
	}
	decodeList := func(s string) string {
		if s == "" || hdr.Decode(s) == s {
			return s
		}
		return joinAddrs(decodeNames(parseList(s)))
	}
	var changed []row
	for _, r := range rows {
		n := row{r.id, hdr.Decode(r.subject), hdr.Decode(r.fromName), decodeList(r.toList), decodeList(r.cc)}
		if n != r {
			changed = append(changed, n)
		}
	}
	if len(changed) == 0 {
		return nil
	}
	return d.tx(ctx, func(tx *sql.Tx) error {
		for _, r := range changed {
			if _, err := tx.ExecContext(ctx, `
				UPDATE messages SET subject = ?, from_name = ?, to_list = ?, cc_list = ? WHERE id = ?`,
				r.subject, r.fromName, r.toList, r.cc, r.id); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `
				UPDATE message_text SET subject = m.subject,
				       people = m.from_name || ' ' || m.from_addr || ' ' || m.to_list || ' ' || m.cc_list
				FROM messages m WHERE m.id = ? AND message_text.rowid = m.id`, r.id); err != nil {
				return err
			}
		}
		return nil
	})
}
