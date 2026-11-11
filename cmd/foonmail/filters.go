package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image/color"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"

	"gioui.org/font"
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"foonmail/internal/cache"
	"foonmail/internal/config"
	"foonmail/internal/rpc"
	"foonmail/internal/sieve"
)

// Shift+F opens the account's mail filters: rules the server applies to
// arriving mail, kept as a Sieve script it reads over ManageSieve. n adds
// a rule, prefilled from the message Shift+F was pressed on; Enter edits
// one, dd deletes it, Shift+j/k change the order (the first match wins),
// and e opens the whole script in the external editor.

// filterView is the filter list and its form.
type filterView struct {
	account  string
	returnTo mode
	tag      *int
	list     widget.List

	loading bool
	saving  bool
	err     string       // why the filters couldn't be read
	script  string       // the script as on the server
	other   string       // another script that is active instead
	rules   []sieve.Rule // nil with byHand
	byHand  bool         // the script isn't foonmail's rules: shown as text
	draft   string       // text the server rejected, to edit again

	sel      int
	deleting int // rule number (from 1) waiting for a second d

	sample sample      // the message Shift+F was pressed on
	form   *filterForm // nil unless adding or editing a rule
	offer  *moveOffer  // after saving a rule made from a message: y moves the matches
}

// sample is what a new rule is prefilled from.
type sample struct {
	addr, subject string
	ok            bool
}

// moveOffer is the matching mail already in the folder.
type moveOffer struct {
	convs []*conversation
	dest  cache.Folder
	label string
}

// filterForm is a rule being written.
type filterForm struct {
	edit    int // index of the rule being edited; -1 for a new one
	field   int // index into sieve.Fields
	match   *int
	value   widget.Editor
	folder  widget.Editor
	read    *int
	markRd  bool
	touched bool // the value was typed, not filled in from the sample
	choice  int  // the highlighted folder suggestion
}

func newFilterForm() *filterForm {
	f := &filterForm{edit: -1, match: new(int), read: new(int)}
	f.value.SingleLine, f.value.Submit = true, true
	f.folder.SingleLine, f.folder.Submit = true, true
	return f
}

// openFilters shows the filters of the account on screen.
func (u *ui) openFilters() {
	if u.daemon == nil {
		u.notice = "foonmaild isn't running: filters are on the server"
		return
	}
	fv := &filterView{account: u.account.Name, returnTo: u.mode, tag: new(int), loading: true}
	fv.list.Axis = layout.Vertical
	fv.sample = u.filterSample()
	u.filters = fv
	u.mode = modeFilters
	u.focusReq = fv.tag
	u.notice = ""
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		script, other, err := u.daemon.GetFilters(ctx, fv.account)
		u.post(func() {
			fv.loading = false
			if err != nil {
				fv.err = filterErr(err)
				return
			}
			fv.setScript(script)
			fv.other = other
		})
	}()
}

func (fv *filterView) setScript(script string) {
	fv.script = script
	fv.rules, fv.byHand = sieve.Parse(script)
	if script == "" {
		fv.rules, fv.byHand = nil, false
	}
	fv.sel = min(fv.sel, max(len(fv.rules)-1, 0))
}

func filterErr(err error) string {
	switch {
	case errors.Is(err, rpc.ErrUnsupported):
		return "This account has no server-side filters (its server has no ManageSieve)."
	case errors.Is(err, rpc.ErrNotRunning):
		return "foonmaild isn't running."
	}
	return err.Error()
}

// filterSample takes the sender and subject of the message Shift+F was
// pressed on: the one being read, or the newest from someone else in the
// selected conversation.
func (u *ui) filterSample() sample {
	var c *conversation
	if u.mode == modeView && u.conv != nil {
		if o := u.conv; o.cur >= 0 && o.cur < len(o.parts) {
			e := o.parts[o.cur].entry
			if !config.IsOwn(u.own, e.From.Addr) {
				return sample{addr: strings.ToLower(e.From.Addr), subject: baseSubject(e.Subject), ok: true}
			}
		}
		c = &u.conv.conv
	} else {
		c = u.current()
	}
	if c == nil {
		return sample{}
	}
	if _, addr, ok := u.sender(c); ok {
		return sample{addr: addr, subject: c.subject, ok: true}
	}
	return sample{}
}

// suggest is the sample's value for a field.
func (s sample) suggest(f sieve.Field) string {
	switch f {
	case sieve.FromAddress:
		return s.addr
	case sieve.FromDomain:
		return domain(s.addr)
	case sieve.Subject:
		return s.subject
	}
	return ""
}

func (u *ui) leaveFilters() {
	fv := u.filters
	u.filters = nil
	u.mode = fv.returnTo
	if u.mode == modeList {
		u.focusReq = u.listTag
	}
}

// startForm opens the form for rule i, or for a new rule with i < 0.
func (u *ui) startForm(i int) {
	fv := u.filters
	f := newFilterForm()
	if i >= 0 && i < len(fv.rules) {
		r := fv.rules[i]
		f.edit = i
		f.field = max(slices.Index(sieve.Fields, r.Field), 0)
		f.value.SetText(r.Value)
		f.folder.SetText(r.Folder)
		f.markRd = r.MarkRead
		f.touched = true
	} else if fv.sample.ok {
		f.value.SetText(fv.sample.suggest(sieve.Fields[0]))
	}
	fv.form = f
	fv.offer = nil
	u.notice = ""
	u.focusReq = &f.value
	if i < 0 && fv.sample.ok {
		u.focusReq = &f.folder // the match is filled in; where it goes isn't
	}
}

// folderNames are the account's folders, as the server names them.
func (u *ui) folderNames() []cache.Folder {
	fs, err := u.db.Folders(context.Background(), u.accountID)
	if err != nil {
		return nil
	}
	return fs
}

// folderSuggestions are the folders whose name contains what is typed.
func (u *ui) folderSuggestions(typed string) []string {
	typed = strings.ToLower(strings.TrimSpace(typed))
	var out []string
	for _, f := range u.folderNames() {
		if strings.Contains(strings.ToLower(f.Name), typed) {
			out = append(out, f.Name)
		}
	}
	return out[:min(len(out), 6)]
}

// submitForm saves the rule, or first takes the highlighted folder when
// what is typed isn't a folder's name yet.
func (u *ui) submitForm() {
	fv := u.filters
	f := fv.form
	typed := strings.TrimSpace(f.folder.Text())
	if s := u.folderSuggestions(typed); len(s) > 0 && !slices.Contains(s, typed) && f.choice < len(s) {
		f.folder.SetText(s[f.choice])
		f.folder.SetCaret(f.folder.Len(), f.folder.Len())
		u.focusReq = &f.folder
		return
	}
	r := sieve.Rule{Field: sieve.Fields[f.field], Value: strings.TrimSpace(f.value.Text()),
		Folder: typed, MarkRead: f.markRd}
	if msg := r.Valid(); msg != "" {
		u.notice = "Can't save: " + msg
		return
	}
	rules := slices.Clone(fv.rules)
	if f.edit >= 0 && f.edit < len(rules) {
		rules[f.edit] = r
	} else {
		rules = append(rules, r)
	}
	fromSample := f.edit < 0 && fv.sample.ok
	u.saveRules(rules, func() {
		fv.form = nil
		fv.sel = slices.Index(fv.rules, r)
		u.focusReq = fv.tag
		u.notice = "Saved: " + r.String()
		if !slices.ContainsFunc(u.folderNames(), func(x cache.Folder) bool { return x.Name == r.Folder }) {
			u.notice += " (the folder is created when the first message arrives)"
		}
		if fromSample {
			u.offerMove(r)
		}
	})
}

// saveRules writes rules to the server; done runs once they are there.
func (u *ui) saveRules(rules []sieve.Rule, done func()) {
	u.saveScript(sieve.Script(rules), done)
}

func (u *ui) saveScript(script string, done func()) {
	fv := u.filters
	fv.saving = true
	u.notice = "Saving the filters…"
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		err := u.daemon.PutFilters(ctx, fv.account, script)
		u.post(func() {
			fv.saving = false
			if err != nil {
				u.notice = "The filters weren't saved: " + filterErr(err)
				if errors.Is(err, rpc.ErrRejected) {
					fv.draft = script
				}
				return
			}
			fv.draft, fv.other = "", ""
			fv.setScript(script)
			if done != nil {
				done()
			} else {
				u.notice = "Filters saved"
			}
		})
	}()
}

// offerMove finds the conversations in the folder being shown that the
// new rule matches, so y can move them now. Only what the cache knows can
// be matched: sender and subject.
func (u *ui) offerMove(r sieve.Rule) {
	fv := u.filters
	dest, ok := cache.Folder{}, false
	for _, f := range u.folderNames() {
		if f.Name == r.Folder {
			dest, ok = f, true
		}
	}
	if !ok || dest.ID == u.folder.ID || u.showingResults() {
		return
	}
	var convs []*conversation
	for i := range u.convs {
		c := &u.convs[i]
		if slices.ContainsFunc(c.members, func(m cache.Entry) bool {
			return m.FolderID == u.folder.ID && ruleMatches(r, m)
		}) {
			convs = append(convs, c)
		}
	}
	if len(convs) == 0 {
		return
	}
	fv.offer = &moveOffer{convs: convs, dest: dest, label: dest.Name}
	u.notice += fmt.Sprintf(". y moves the %s in %s that match to %s too", conversations(len(convs)), u.folder.Name, dest.Name)
}

// ruleMatches is the rule's test on what the cache has of a message.
func ruleMatches(r sieve.Rule, m cache.Entry) bool {
	v := strings.ToLower(strings.TrimSpace(r.Value))
	switch r.Field {
	case sieve.FromAddress:
		return strings.EqualFold(m.From.Addr, v)
	case sieve.FromDomain:
		return strings.EqualFold(domain(m.From.Addr), v)
	case sieve.Subject:
		return strings.Contains(strings.ToLower(m.Subject), v)
	}
	return false
}

// editScript opens the script in the external editor and saves what
// comes back; the server checks it.
func (u *ui) editScript() {
	fv := u.filters
	text := fv.draft
	if text == "" {
		text = fv.script
	}
	if text == "" {
		text = sieve.Script(nil)
	}
	argv := u.editorArgv()
	fv.saving = true
	u.notice = "Editing the filters in " + strings.Join(argv, " ") + "…"
	go func() {
		edited, err := editText(argv, text, "foonmail-filters-*.sieve")
		u.post(func() {
			fv.saving = false
			switch {
			case err != nil:
				u.notice = "External editor failed: " + err.Error()
			case edited == text:
				u.notice = "The external editor closed without changes."
			default:
				u.saveScript(edited, nil)
			}
		})
	}()
}

// editText lets the user edit text in the external editor.
func editText(argv []string, text, pattern string) (string, error) {
	if len(argv) == 0 {
		return text, errors.New("no editor command")
	}
	f, err := os.CreateTemp(os.Getenv("XDG_RUNTIME_DIR"), pattern)
	if err != nil {
		return text, err
	}
	path := f.Name()
	f.Close()
	defer os.Remove(path)
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		return text, err
	}
	cmd := exec.Command(argv[0], append(argv[1:], path)...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return text, fmt.Errorf("%s: %w", argv[0], err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return text, err
	}
	return string(bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n"))), nil
}

// updateFilters handles the filter view's keys.
func (u *ui) updateFilters(gtx layout.Context) {
	fv := u.filters
	t := fv.tag
	if fv.form != nil {
		u.updateForm(gtx)
		return
	}
	// Without the form the list is the only thing to focus; it takes the
	// keys again once the form closes.
	defer func() {
		if u.filters == fv && fv.form == nil && u.focusReq == nil && !gtx.Focused(t) {
			u.focusReq = t
		}
	}()
	for {
		ev, ok := gtx.Event(
			key.FocusFilter{Target: t},
			key.Filter{Focus: t, Name: "J", Optional: key.ModShift},
			key.Filter{Focus: t, Name: "K", Optional: key.ModShift},
			key.Filter{Focus: t, Name: key.NameDownArrow, Optional: key.ModShift},
			key.Filter{Focus: t, Name: key.NameUpArrow, Optional: key.ModShift},
			key.Filter{Focus: t, Name: key.NameReturn},
			key.Filter{Focus: t, Name: key.NameEnter},
			key.Filter{Focus: t, Name: "N"},
			key.Filter{Focus: t, Name: "D"},
			key.Filter{Focus: t, Name: key.NameDeleteForward},
			key.Filter{Focus: t, Name: "E"},
			key.Filter{Focus: t, Name: "Y"},
			key.Filter{Focus: t, Name: "Q"},
			key.Filter{Focus: t, Name: key.NameEscape},
		)
		if !ok {
			break
		}
		ke, ok := ev.(key.Event)
		if !ok || ke.State != key.Press {
			continue
		}
		if fv.saving || fv.loading {
			if ke.Name == key.NameEscape {
				u.leaveFilters()
				return
			}
			continue
		}
		if ke.Name != "D" && ke.Name != key.NameDeleteForward {
			fv.deleting = 0
		}
		offer := fv.offer
		fv.offer = nil
		shift := ke.Modifiers.Contain(key.ModShift)
		editable := fv.err == "" && !fv.byHand
		switch ke.Name {
		case "J", key.NameDownArrow:
			if shift && editable {
				u.moveRule(fv.sel, fv.sel+1)
			} else {
				fv.sel = min(fv.sel+1, max(len(fv.rules)-1, 0))
			}
		case "K", key.NameUpArrow:
			if shift && editable {
				u.moveRule(fv.sel, fv.sel-1)
			} else {
				fv.sel = max(fv.sel-1, 0)
			}
		case key.NameReturn, key.NameEnter:
			if editable && fv.sel < len(fv.rules) {
				u.startForm(fv.sel)
			}
		case "N":
			if editable {
				u.startForm(-1)
			}
		case "D", key.NameDeleteForward:
			if !editable || fv.sel >= len(fv.rules) {
				break
			}
			if fv.deleting != fv.sel+1 {
				fv.deleting = fv.sel + 1
				u.notice = "d again deletes: " + fv.rules[fv.sel].String()
				break
			}
			fv.deleting = 0
			gone := fv.rules[fv.sel]
			rules := slices.Delete(slices.Clone(fv.rules), fv.sel, fv.sel+1)
			u.saveRules(rules, func() { u.notice = "Deleted: " + gone.String() })
		case "E":
			if fv.err == "" {
				u.editScript()
			}
		case "Y":
			if offer != nil {
				u.notice = ""
				u.leaveFilters()
				u.moveConvs(offer.convs, offer.dest, offer.label, false)
				return
			}
		case "Q", key.NameEscape:
			u.notice = ""
			u.leaveFilters()
			return
		}
	}
}

// moveRule moves rule i to j and saves.
func (u *ui) moveRule(i, j int) {
	fv := u.filters
	if i < 0 || j < 0 || i >= len(fv.rules) || j >= len(fv.rules) {
		return
	}
	rules := slices.Clone(fv.rules)
	rules[i], rules[j] = rules[j], rules[i]
	fv.sel = j
	u.saveRules(rules, func() { fv.sel = j; u.notice = "" })
}

// updateForm handles the rule form: Tab moves between its parts, ←/→
// choose what to match, Space marks read, ↑/↓ choose a folder, Enter
// saves, Esc cancels.
func (u *ui) updateForm(gtx layout.Context) {
	fv := u.filters
	f := fv.form
	for _, e := range []*widget.Editor{&f.value, &f.folder} {
		e.ReadOnly = fv.saving
		for {
			ev, ok := e.Update(gtx)
			if !ok {
				break
			}
			switch ev.(type) {
			case widget.ChangeEvent:
				if e == &f.value {
					f.touched = true
				} else {
					f.choice = 0
				}
			case widget.SubmitEvent:
				if !fv.saving {
					u.submitForm()
				}
			}
		}
	}
	for {
		ev, ok := gtx.Event(
			key.FocusFilter{Target: f.match},
			key.FocusFilter{Target: f.read},
			key.Filter{Focus: f.match, Name: key.NameLeftArrow},
			key.Filter{Focus: f.match, Name: key.NameRightArrow},
			key.Filter{Focus: f.match, Name: key.NameReturn},
			key.Filter{Focus: f.match, Name: key.NameEnter},
			key.Filter{Focus: f.read, Name: key.NameSpace},
			key.Filter{Focus: f.read, Name: key.NameReturn},
			key.Filter{Focus: f.read, Name: key.NameEnter},
			key.Filter{Focus: &f.folder, Name: key.NameDownArrow},
			key.Filter{Focus: &f.folder, Name: key.NameUpArrow},
			key.Filter{Name: key.NameEscape},
		)
		if !ok {
			break
		}
		ke, ok := ev.(key.Event)
		if !ok || ke.State != key.Press || fv.saving {
			continue
		}
		switch ke.Name {
		case key.NameLeftArrow, key.NameRightArrow:
			d := 1
			if ke.Name == key.NameLeftArrow {
				d = len(sieve.Fields) - 1
			}
			f.field = (f.field + d) % len(sieve.Fields)
			if !f.touched && fv.sample.ok {
				f.value.SetText(fv.sample.suggest(sieve.Fields[f.field]))
			}
		case key.NameSpace:
			f.markRd = !f.markRd
		case key.NameDownArrow:
			f.choice++
		case key.NameUpArrow:
			f.choice = max(f.choice-1, 0)
		case key.NameReturn, key.NameEnter:
			u.submitForm()
		case key.NameEscape:
			fv.form = nil
			u.notice = ""
			u.focusReq = fv.tag
			return
		}
	}
	if n := len(u.folderSuggestions(f.folder.Text())); f.choice >= n {
		f.choice = max(n-1, 0)
	}
}

func (u *ui) filterHints() string {
	fv := u.filters
	switch {
	case fv.form != nil:
		return "Tab next part · ←/→ what to match · ↑/↓ choose a folder · Space mark read · Enter save · Esc cancel"
	case fv.loading:
		return "Reading the filters from the server… · Esc back"
	case fv.err != "":
		return "Esc back"
	case fv.byHand:
		return "e edit the script in your editor · Esc back"
	}
	h := "j/k choose · Enter edit · n new"
	if fv.sample.ok {
		h += " (like the selected message)"
	}
	return h + " · d delete · Shift+j/k move up/down (the first match wins) · e edit as text · Esc back"
}

// layoutFilters draws the filter view.
func (u *ui) layoutFilters(gtx layout.Context) layout.Dimensions {
	fv := u.filters
	children := []layout.FlexChild{
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			title := "Filters · " + fv.account
			if !fv.loading && fv.err == "" && !fv.byHand {
				title += " · " + plural(len(fv.rules), "rule")
			}
			return layout.Inset{Top: 10, Bottom: 10, Left: 12, Right: 12}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				l := material.Label(u.th, 16, title)
				l.Font.Weight = font.Bold
				return l.Layout(gtx)
			})
		}),
		layout.Rigid(u.divider),
		layout.Flexed(1, u.filterBody),
	}
	if fv.form != nil {
		children = append(children, layout.Rigid(u.divider), layout.Rigid(u.filterFormLayout))
	}
	children = append(children, layout.Rigid(u.statusBar(u.filterHints())))
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx, children...)
}

func (u *ui) filterBody(gtx layout.Context) layout.Dimensions {
	fv := u.filters
	size := gtx.Constraints.Max
	defer clip.Rect{Max: size}.Push(gtx.Ops).Pop()
	event.Op(gtx.Ops, fv.tag)
	muted := func(s string) layout.Widget {
		return func(gtx layout.Context) layout.Dimensions {
			l := material.Label(u.th, u.th.TextSize, s)
			l.Color = u.muted
			return layout.UniformInset(12).Layout(gtx, l.Layout)
		}
	}
	var lines []layout.Widget
	switch {
	case fv.loading:
		lines = append(lines, muted("Reading the filters…"))
	case fv.err != "":
		lines = append(lines, muted(fv.err))
	default:
		if fv.other != "" {
			lines = append(lines, muted(fmt.Sprintf("The script “%s” is active on the server now. Saving these filters makes them active instead.", fv.other)))
		}
		if fv.byHand {
			lines = append(lines, muted("The filters were changed outside foonmail's rules, so they show as the script itself. e edits it."))
			for _, l := range strings.Split(strings.TrimRight(fv.script, "\n"), "\n") {
				lines = append(lines, u.monoLine(l))
			}
		} else if len(fv.rules) == 0 {
			lines = append(lines, muted("No filters yet. n adds one."))
		}
		for i, r := range fv.rules {
			if fv.byHand {
				break
			}
			lines = append(lines, u.ruleRow(i, r))
		}
	}
	material.List(u.th, &fv.list).Layout(gtx, len(lines), func(gtx layout.Context, i int) layout.Dimensions {
		return lines[i](gtx)
	})
	return layout.Dimensions{Size: size}
}

func (u *ui) monoLine(s string) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		l := material.Label(u.th, u.th.TextSize*0.9, s)
		l.Font = u.mono
		return layout.Inset{Left: 12, Right: 12}.Layout(gtx, l.Layout)
	}
}

func (u *ui) ruleRow(i int, r sieve.Rule) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		fv := u.filters
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		bg := color.NRGBA{}
		if i == fv.sel && fv.form == nil {
			bg = u.selBgBlurred
			if gtx.Focused(fv.tag) {
				bg = u.selBg
			}
		}
		return withBackground(gtx, bg, func(gtx layout.Context) layout.Dimensions {
			return u.ruleRowContent(gtx, i, r)
		})
	}
}

func (u *ui) ruleRowContent(gtx layout.Context, i int, r sieve.Rule) layout.Dimensions {
	return layout.Inset{Top: 6, Bottom: 6, Left: 12, Right: 12}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Alignment: layout.Baseline}.Layout(gtx,
			layout.Rigid(u.cell(36, fmt.Sprint(i+1), font.Font{}, u.muted)),
			layout.Rigid(u.cell(150, r.Field.Label(), font.Font{}, u.muted)),
			layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
				s := r.Value + "  →  " + r.Folder
				if r.MarkRead {
					s += "  (marked read)"
				}
				return material.Label(u.th, u.th.TextSize, s).Layout(gtx)
			}),
		)
	})
}

// withBackground draws w over bg (nothing when bg is transparent).
func withBackground(gtx layout.Context, bg color.NRGBA, w layout.Widget) layout.Dimensions {
	macro := op.Record(gtx.Ops)
	d := w(gtx)
	call := macro.Stop()
	if bg.A > 0 {
		paint.FillShape(gtx.Ops, bg, clip.Rect{Max: d.Size}.Op())
	}
	call.Add(gtx.Ops)
	return d
}

// filterFormLayout draws the rule form under the list.
func (u *ui) filterFormLayout(gtx layout.Context) layout.Dimensions {
	f := u.filters.form
	label := func(s string) layout.FlexChild {
		return layout.Rigid(u.cell(90, s, font.Font{}, u.muted))
	}
	row := func(children ...layout.FlexChild) layout.Widget {
		return func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Top: 4, Bottom: 4}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Alignment: layout.Baseline}.Layout(gtx, children...)
			})
		}
	}
	focusRing := func(tag *int, w layout.Widget) layout.Widget {
		return func(gtx layout.Context) layout.Dimensions {
			m := clip.Rect{Max: gtx.Constraints.Max}.Push(gtx.Ops)
			event.Op(gtx.Ops, tag)
			m.Pop()
			bg := color.NRGBA{}
			if gtx.Focused(tag) {
				bg = u.selBg
			}
			return withBackground(gtx, bg, w)
		}
	}
	matchW := focusRing(f.match, func(gtx layout.Context) layout.Dimensions {
		var items []layout.FlexChild
		for i, fl := range sieve.Fields {
			s, c, w := fl.Label(), u.muted, font.Normal
			if i == f.field {
				c, w = u.th.Fg, font.Bold
			}
			items = append(items, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				l := material.Label(u.th, u.th.TextSize, s)
				l.Color, l.Font.Weight = c, w
				return layout.Inset{Right: 16}.Layout(gtx, l.Layout)
			}))
		}
		return layout.Flex{}.Layout(gtx, items...)
	})
	readW := focusRing(f.read, func(gtx layout.Context) layout.Dimensions {
		box := "[ ]"
		if f.markRd {
			box = "[x]"
		}
		return material.Label(u.th, u.th.TextSize, box+" also mark it read").Layout(gtx)
	})
	sugg := u.folderSuggestions(f.folder.Text())
	typed := strings.TrimSpace(f.folder.Text())
	rows := []layout.Widget{
		row(label("Match"), layout.Flexed(1, matchW)),
		row(label("Value"), layout.Flexed(1, material.Editor(u.th, &f.value, "what to match").Layout)),
		row(label("Folder"), layout.Flexed(1, material.Editor(u.th, &f.folder, "where it goes (type to choose)").Layout)),
	}
	if len(sugg) > 0 && !slices.Contains(sugg, typed) {
		var items []layout.FlexChild
		for i, s := range sugg {
			c := u.muted
			if i == f.choice {
				c = u.accent
			}
			items = append(items, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				l := material.Label(u.th, u.th.TextSize*0.9, s)
				l.Color = c
				return layout.Inset{Right: 16}.Layout(gtx, l.Layout)
			}))
		}
		rows = append(rows, row(label(""), layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{}.Layout(gtx, items...)
		})))
	}
	rows = append(rows, row(label(""), layout.Flexed(1, readW)))
	return layout.Inset{Top: 8, Bottom: 8, Left: 12, Right: 12}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		var children []layout.FlexChild
		for _, r := range rows {
			children = append(children, layout.Rigid(r))
		}
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx, children...)
	})
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}
