package cache

import (
	"context"
	"regexp"
	"strings"
	"time"
)

// Correspondent is someone mail was exchanged with.
type Correspondent struct {
	Name, Addr string
	// Sent counts the messages from you that went to them; Received, the
	// messages from them.
	Sent, Received int
	Last           time.Time // the latest of those messages
}

// listAddr picks the addresses out of a stored "Name <addr>, ..." list.
// Names aren't quoted there, so one with a comma loses its first part,
// but every address is found.
var listAddr = regexp.MustCompile(`(?:([^,<>]*?)\s*<([^<>\s]+@[^<>\s]+)>|([^\s,<>]+@[^\s,<>]+))`)

func parseList(s string) []Address {
	var out []Address
	for _, m := range listAddr.FindAllStringSubmatch(s, -1) {
		if m[2] != "" {
			out = append(out, Address{Name: strings.Trim(strings.TrimSpace(m[1]), `"`), Addr: m[2]})
		} else {
			out = append(out, Address{Addr: m[3]})
		}
	}
	return out
}

// Correspondents lists everyone in an account's cached mail: the
// recipients of messages from one of own (your addresses) and the senders
// of the rest, excluding yourself. Each keeps the latest name seen with
// the address.
func (d *DB) Correspondents(ctx context.Context, accountID int64, own []string) ([]Correspondent, error) {
	mine := map[string]bool{}
	for _, a := range own {
		mine[strings.ToLower(a)] = true
	}
	rows, err := d.db.QueryContext(ctx, `
		SELECT from_name, from_addr, to_list, cc_list, date FROM messages
		WHERE account_id = ? ORDER BY date`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byAddr := map[string]*Correspondent{}
	var order []string
	note := func(a Address, when time.Time, sent bool) {
		key := strings.ToLower(strings.TrimSpace(a.Addr))
		if key == "" || mine[key] {
			return
		}
		c := byAddr[key]
		if c == nil {
			c = &Correspondent{Addr: strings.TrimSpace(a.Addr)}
			byAddr[key] = c
			order = append(order, key)
		}
		if a.Name != "" && a.Name != a.Addr {
			c.Name = a.Name // rows come oldest first: the latest name wins
		}
		if sent {
			c.Sent++
		} else {
			c.Received++
		}
		if when.After(c.Last) {
			c.Last = when
		}
	}
	for rows.Next() {
		var fromName, fromAddr, to, cc string
		var date int64
		if err := rows.Scan(&fromName, &fromAddr, &to, &cc, &date); err != nil {
			return nil, err
		}
		when := time.Unix(date, 0)
		if mine[strings.ToLower(fromAddr)] {
			for _, a := range append(parseList(to), parseList(cc)...) {
				note(a, when, true)
			}
		} else {
			note(Address{Name: fromName, Addr: fromAddr}, when, false)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]Correspondent, len(order))
	for i, k := range order {
		out[i] = *byAddr[k]
	}
	return out, nil
}
