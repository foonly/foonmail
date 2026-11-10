package main

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"gioui.org/io/key"
	"gioui.org/io/system"
	"gioui.org/layout"

	"foonmail/internal/cache"
	"foonmail/internal/rpc"
)

// Ctrl+Enter in the / filter searches the server instead: every folder of
// the account but Trash and Junk, From, To and Subject first and then the
// whole message. The daemon stores what it finds in the cache, so the
// results are conversations like any other. They replace the list while
// they last; Enter opens one in its own folder, and closing it comes back
// to the results. Esc cancels a search still running and goes back to the
// list as it was, filter included.

// serverSearch is a server search and its results.
type serverSearch struct {
	id      int64 // the daemon's id; 0 until it answers
	query   string
	running bool
	err     string
	started time.Time
	took    time.Duration

	hits   map[movingKey]bool // found messages, by folder and UID
	inBody map[movingKey]bool // found by the body pass only
	order  []int64            // folders with results, as they came

	// Where the list was, to go back to.
	from      cache.Folder
	searching bool
	search    string

	// opened is the result being read, in its own folder; "" while the
	// results show.
	opened string
	names  map[int64]string // folder names, for the rows
}

// showingResults says whether the list shows server search results.
func (u *ui) showingResults() bool { return u.server != nil && u.server.opened == "" }

// startServerSearch searches the server for the filter's words.
func (u *ui) startServerSearch() {
	words := strings.Fields(u.search.Text())
	if len(words) == 0 {
		u.notice = "Type what to search for first"
		return
	}
	if u.daemon == nil {
		u.notice = "foonmaild isn't running: only the cache can be searched"
		return
	}
	u.endServerSearch()
	u.endSimilar()
	s := &serverSearch{
		query: strings.Join(words, " "), running: true, started: u.now(),
		hits: map[movingKey]bool{}, inBody: map[movingKey]bool{},
		from: u.folder, searching: u.searching, search: u.search.Text(),
		names: map[int64]string{},
	}
	if folders, err := u.db.Folders(context.Background(), u.accountID); err == nil {
		for _, f := range folders {
			s.names[f.ID] = f.Name
		}
	}
	u.server = s
	u.clearMarks()
	u.searching = false
	u.mode = modeList
	u.focusReq = u.listTag
	u.list.Position = layout.Position{}
	u.reload()
	u.notice = ""

	account := u.account.Name
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		id, err := u.daemon.SearchServer(ctx, account, words)
		u.post(func() {
			if u.server != s {
				if err == nil {
					go u.daemon.CancelSearch(context.Background(), id)
				}
				return
			}
			if err != nil {
				s.running = false
				s.err = searchError(err)
				return
			}
			s.id = id
		})
	}()
}

func searchError(err error) string {
	switch {
	case errors.Is(err, rpc.ErrOffline):
		return "the account is offline"
	case errors.Is(err, rpc.ErrNotRunning):
		return "foonmaild isn't running"
	}
	return err.Error()
}

// searchFound adds a batch of results.
func (u *ui) searchFound(ev rpc.SearchFound) {
	s := u.server
	if s == nil || ev.Search != s.id {
		return
	}
	for _, uid := range ev.UIDs {
		k := movingKey{ev.FolderID, uid}
		s.hits[k] = true
		if ev.InBody {
			s.inBody[k] = true
		}
	}
	if !slices.Contains(s.order, ev.FolderID) {
		s.order = append(s.order, ev.FolderID)
	}
	if s.opened == "" {
		u.reload()
	}
}

func (u *ui) searchDone(ev rpc.SearchDone) {
	s := u.server
	if s == nil || ev.Search != s.id {
		return
	}
	s.running = false
	s.took = u.now().Sub(s.started)
	if ev.Err != "cancelled" {
		s.err = ev.Err
	}
}

// endServerSearch forgets the results, cancelling the search if it runs.
func (u *ui) endServerSearch() {
	s := u.server
	if s == nil {
		return
	}
	u.server = nil
	if s.running && s.id != 0 && u.daemon != nil {
		go u.daemon.CancelSearch(context.Background(), s.id)
	}
}

// leaveServerSearch goes back to the list as it was before the search.
func (u *ui) leaveServerSearch() {
	s := u.server
	if s == nil {
		return
	}
	u.endServerSearch()
	if s.opened != "" || u.folder.ID != s.from.ID {
		u.folder = s.from
	}
	u.searching = s.searching
	u.search.SetText(s.search)
	u.notice = ""
	u.list.Position = layout.Position{}
	u.reload()
	if u.searching {
		u.focusReq = &u.search
	}
}

// buildResults makes the list from the results: for each folder with
// results, the conversations there that hold one, newest first.
func (u *ui) buildResults() {
	s := u.server
	ctx := context.Background()
	var sent []cache.Entry
	if u.sent != nil {
		sent, _ = u.db.Entries(ctx, u.sent.ID)
	}
	u.entries = nil
	u.convs = u.convs[:0]
	for _, id := range s.order {
		entries, err := u.db.Entries(ctx, id)
		if err != nil {
			u.notice = "Reading the cache failed: " + err.Error()
			return
		}
		if u.sent != nil && id != u.sent.ID {
			entries = append(entries, sent...)
		}
		for _, c := range buildConversations(entries, id, u.own) {
			if slices.ContainsFunc(c.members, func(m cache.Entry) bool { return s.hits[movingKey{m.FolderID, m.UID}] }) {
				u.convs = append(u.convs, c)
			}
		}
	}
	slices.SortStableFunc(u.convs, func(a, b conversation) int { return b.latest.Compare(a.latest) })
}

// onlyInBody says whether a result row was found by its body alone.
func (s *serverSearch) onlyInBody(c *conversation) bool {
	for _, m := range c.members {
		k := movingKey{m.FolderID, m.UID}
		if s.hits[k] && !s.inBody[k] {
			return false
		}
	}
	return true
}

// openResult opens the selected result in its own folder.
func (u *ui) openResult() {
	c := u.current()
	if c == nil {
		return
	}
	f, err := u.db.FolderByID(context.Background(), c.folder)
	if err != nil {
		u.notice = "Reading the cache failed: " + err.Error()
		return
	}
	u.server.opened = c.key
	u.folder = f
	u.reload() // keeps c selected: it is the same conversation
	u.open()
}

// backToResults shows the results again after reading one; layout calls
// it whenever the list shows while a result is open.
func (u *ui) backToResults() {
	s := u.server
	if s == nil || s.opened == "" || u.mode != modeList {
		return
	}
	u.folder = s.from
	s.opened = ""
	u.reload()
}

// resultsHeader is the list header while results show.
func (u *ui) resultsHeader() []string {
	s := u.server
	parts := []string{u.account.Name, fmt.Sprintf("Server: “%s”", s.query)}
	found := fmt.Sprintf("%d conversations in %d folders", len(u.convs), len(s.order))
	switch {
	case s.err != "":
		parts = append(parts, found, "search failed: "+s.err)
	case s.running && s.id == 0:
		parts = append(parts, "starting…")
	case s.running:
		parts = append(parts, found, "searching…")
	default:
		parts = append(parts, found, fmt.Sprintf("done in %.1f s", s.took.Seconds()))
	}
	return parts
}

const resultHints = "j/k choose · Enter open · / change the search · Esc back to the list"

// resultKey handles a list key while results show: moving, opening and
// leaving. Acting on a result happens in the conversation, which is in
// the result's own folder.
func (u *ui) resultKey(ke key.Event, page int) {
	switch ke.Name {
	case "J", key.NameDownArrow:
		u.moveTo(u.sel + 1)
	case "K", key.NameUpArrow:
		u.moveTo(u.sel - 1)
	case key.NamePageDown:
		u.moveTo(u.sel + page)
	case key.NamePageUp:
		u.moveTo(u.sel - page)
	case key.NameHome, "=":
		u.moveTo(0)
	case key.NameEnd, "*":
		u.moveTo(len(u.view) - 1)
	case key.NameReturn, key.NameEnter:
		u.openResult()
	case "/":
		query := u.server.query
		u.leaveServerSearch()
		u.searching = true
		if u.search.Len() == 0 {
			u.search.SetText(query)
		}
		u.search.SetCaret(u.search.Len(), u.search.Len())
		u.focusReq = &u.search
	case key.NameEscape:
		u.leaveServerSearch()
	case "Q":
		if u.w != nil {
			u.w.Perform(system.ActionClose)
		}
	case key.NameTab:
		u.notice = "Esc leaves the server search first"
	default:
		u.notice = "Open the conversation (Enter) to act on it"
	}
}
