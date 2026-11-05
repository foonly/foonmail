package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"gioui.org/font"
	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/widget"
	"gioui.org/widget/material"
	"gioui.org/x/explorer"

	"foonmail/internal/cache"
	"foonmail/internal/compose"
	"foonmail/internal/render"
	"foonmail/internal/rpc"
)

const composeHints = "Ctrl+Enter send · Ctrl+S save to Drafts · Ctrl+E external editor · Ctrl+O attach · " +
	"Ctrl+R remove last attachment · Tab next field · Esc twice discards"

// composer is the message being written.
type composer struct {
	base        *compose.Draft // threading headers and forwarded attachments come from here
	initial     compose.Draft  // to tell whether anything was written
	from, to    widget.Editor
	cc, bcc     widget.Editor
	subject     widget.Editor
	body        widget.Editor
	attachments []compose.Attachment
	answering   *cache.Entry // the message replied to, to mark it \Answered
	draftOf     *cache.Entry // the stored draft being continued, deleted once sent
	returnTo    mode
	busy        bool // sending, picking files or in the external editor
	discarding  bool // Esc was pressed once with something written

	// Address suggestions for the entry being typed in suggField.
	sugg      []contact
	suggSel   int
	suggField *widget.Editor
	suggEntry string
	suggOff   bool // Esc closed them until the next edit

	dirty    bool      // changed since the last autosave
	editedAt time.Time // the last change
}

// edited notes a change, for autosave.
func (c *composer) edited(now time.Time) { c.dirty, c.editedAt = true, now }

func (c *composer) draft() *compose.Draft {
	d := *c.base
	d.From, d.To, d.Cc, d.Bcc = c.from.Text(), c.to.Text(), c.cc.Text(), c.bcc.Text()
	d.Subject, d.Body = c.subject.Text(), c.body.Text()
	d.Attachments = slices.Clone(c.attachments)
	return &d
}

func (c *composer) set(d *compose.Draft) {
	c.from.SetText(d.From)
	c.to.SetText(d.To)
	c.cc.SetText(d.Cc)
	c.bcc.SetText(d.Bcc)
	c.subject.SetText(d.Subject)
	c.body.SetText(d.Body)
	c.attachments = slices.Clone(d.Attachments)
}

func (c *composer) changed() bool {
	d := c.draft()
	i := c.initial
	return d.From != i.From || d.To != i.To || d.Cc != i.Cc || d.Bcc != i.Bcc || d.Subject != i.Subject ||
		d.Body != i.Body || len(d.Attachments) != len(i.Attachments)
}

// startCompose opens the compose view on a draft.
func (u *ui) startCompose(d *compose.Draft, answering *cache.Entry) {
	c := &composer{base: d, initial: *d, answering: answering, returnTo: u.mode}
	for _, e := range []*widget.Editor{&c.from, &c.to, &c.cc, &c.bcc, &c.subject} {
		e.SingleLine = true
	}
	c.set(d)
	u.comp, u.mode = c, modeCompose
	u.loadContacts()
	if strings.TrimSpace(d.To) == "" {
		u.focusReq = &c.to
	} else {
		u.focusReq = &c.body
		c.body.SetCaret(0, 0)
	}
}

// newMessage starts an empty message.
func (u *ui) newMessage() { u.startCompose(compose.New(u.account), nil) }

type replyKind int

const (
	kindReply replyKind = iota
	kindReplyAll
	kindForward
)

// replyTo starts a reply or forward of a message, loading and rendering it
// first if needed.
func (u *ui) replyTo(e cache.Entry, rendered *render.Message, kind replyKind) {
	build := func(raw []byte, r *render.Message) {
		var d *compose.Draft
		var err error
		switch kind {
		case kindReply:
			d = compose.Reply(u.account, r, compose.ReplySender)
		case kindReplyAll:
			d = compose.Reply(u.account, r, compose.ReplyAll)
		case kindForward:
			d, err = compose.Forward(u.account, r, raw)
		}
		if err != nil {
			u.notice = "Couldn't forward: " + err.Error()
			return
		}
		var answering *cache.Entry
		if kind != kindForward && e.FolderID == u.folder.ID {
			answering = &e
		}
		u.startCompose(d, answering)
	}
	// Converted HTML reads the same when quoted; plain text shown has its
	// addresses shortened, so it is rendered again as written.
	if rendered != nil && rendered.Source == render.SourceHTML && kind != kindForward {
		build(nil, rendered)
		return
	}
	u.notice = "Loading the message…"
	go func() {
		raw, err := u.loadRaw(e.Summary)
		var r *render.Message
		if err == nil {
			r, err = render.Render(raw, render.Options{})
		}
		u.post(func() {
			if err != nil {
				u.notice = "Couldn't load the message: " + err.Error()
				return
			}
			u.notice = ""
			build(raw, r)
		})
	}()
}

// replyFromList replies to the newest message of the selected conversation
// that isn't yours (or the newest, if they all are).
func (u *ui) replyFromList(kind replyKind) {
	c := u.current()
	if c == nil {
		return
	}
	pick := c.members[len(c.members)-1]
	for i := len(c.members) - 1; i >= 0; i-- {
		if !slices.Contains(u.own, strings.ToLower(c.members[i].From.Addr)) {
			pick = c.members[i]
			break
		}
	}
	u.replyTo(pick, nil, kind)
}

// replyFromView replies to the current message of the conversation view.
func (u *ui) replyFromView(kind replyKind) {
	o := u.conv
	if o == nil || len(o.parts) == 0 {
		return
	}
	p := o.parts[min(max(o.cur, 0), len(o.parts)-1)]
	u.replyTo(p.entry, p.rendered, kind)
}

func (u *ui) leaveCompose() {
	c := u.comp
	u.comp = nil
	// Sent, saved to Drafts or discarded: the copy kept in case of a crash
	// has done its job.
	u.removeAutosave()
	u.mode = c.returnTo
	switch u.mode {
	case modeList:
		u.focusReq = u.listTag
	}
}

// sendComposed builds the message and hands it to the daemon's outbox.
func (u *ui) sendComposed() {
	c := u.comp
	d := c.draft()
	raw, err := compose.Build(d)
	if err != nil {
		u.notice = "Can't send yet: " + err.Error()
		return
	}
	if u.daemon == nil {
		u.notice = "foonmaild isn't running: can't send"
		return
	}
	c.busy = true
	u.notice = "Sending…"
	account, to := u.account.Name, d.To
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, err := u.daemon.Send(ctx, account, raw)
		u.post(func() {
			c.busy = false
			switch {
			case errors.Is(err, rpc.ErrNotRunning):
				u.notice = "foonmaild isn't running: the message wasn't sent"
			case errors.Is(err, rpc.ErrTooLarge):
				u.notice = "Too large for the server: " + err.Error()
			case err != nil:
				u.notice = "Couldn't send: " + err.Error()
			default:
				if c.answering != nil {
					u.markAnswered(*c.answering)
				}
				if c.draftOf != nil {
					u.deleteDraft(*c.draftOf)
				}
				if u.comp == c {
					u.leaveCompose()
				}
				u.notice = "Sending to " + firstRecipient(to) + " · undo from the notification within 10 seconds"
			}
		})
	}()
}

// markAnswered flags the replied-to message \Answered, as mutt does.
func (u *ui) markAnswered(e cache.Entry) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := u.daemon.SetFlags(ctx, e.FolderID, []uint32{e.UID}, []string{`\Answered`}, nil); err != nil {
			u.log.Debug("marking answered", "err", err)
		}
	}()
}

func firstRecipient(list string) string {
	first, rest, _ := strings.Cut(list, ",")
	if strings.TrimSpace(rest) != "" {
		return strings.TrimSpace(first) + " and others"
	}
	return strings.TrimSpace(first)
}

// editExternally hands the draft to the external editor in the background.
func (u *ui) editExternally() {
	c := u.comp
	argv := u.editorArgv()
	c.busy = true
	u.notice = "Editing in " + strings.Join(argv, " ") + "…"
	d := c.draft()
	go func() {
		nd, outcome, err := compose.EditExternally(argv, d)
		u.post(func() {
			c.busy = false
			switch {
			case err != nil:
				u.notice = "External editor failed: " + err.Error()
			case outcome == compose.EditUnchanged:
				u.notice = "The external editor closed without changes."
			case outcome == compose.EditCancelled:
				u.notice = "External edit abandoned; the message here is unchanged."
			default:
				c.base.InReplyTo, c.base.References = nd.InReplyTo, nd.References
				c.set(nd)
				c.edited(u.now())
				u.notice = "Updated from the external editor."
			}
		})
	}()
}

// defaultEditor is FOONMAIL_EDITOR, or $VISUAL/$EDITOR (or micro) in a foot
// terminal window.
func defaultEditor() []string {
	if f := strings.Fields(os.Getenv("FOONMAIL_EDITOR")); len(f) > 0 {
		return f
	}
	ed := os.Getenv("VISUAL")
	if ed == "" {
		ed = os.Getenv("EDITOR")
	}
	if ed == "" {
		ed = "micro"
	}
	return append([]string{"foot"}, strings.Fields(ed)...)
}

// pickFiles attaches files chosen in the desktop's file picker.
func (u *ui) pickFiles() {
	c := u.comp
	if u.expl == nil {
		u.notice = "No file picker here; use Attach: lines in the external editor (Ctrl+E)"
		return
	}
	c.busy = true
	u.notice = "Choosing files…"
	go func() {
		files, err := u.expl.ChooseFiles()
		var paths []string
		for _, f := range files {
			if of, ok := f.(*os.File); ok {
				paths = append(paths, of.Name())
			}
			f.Close()
		}
		u.post(func() {
			c.busy = false
			switch {
			case errors.Is(err, explorer.ErrUserDecline):
				u.notice = ""
			case err != nil:
				u.notice = "File picker failed: " + err.Error()
			default:
				for _, p := range paths {
					c.attachments = append(c.attachments, compose.Attachment{Name: baseName(p), Path: p})
				}
				c.edited(u.now())
				u.notice = fmt.Sprintf("Attached %d file(s).", len(paths))
			}
		})
	}()
}

func baseName(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[i+1:]
	}
	return p
}

// Events and layout

func (u *ui) updateCompose(gtx layout.Context) {
	c := u.comp
	// The suggestion list takes its keys before the address field does.
	u.suggestionKeys(gtx)
	// Editor changes first: an edit made before an Esc must not cancel the
	// "press Esc again" that follows it.
	for _, e := range []*widget.Editor{&c.from, &c.to, &c.cc, &c.bcc, &c.subject, &c.body} {
		e.ReadOnly = c.busy
		for {
			ev, ok := e.Update(gtx)
			if !ok {
				break
			}
			if _, ok := ev.(widget.ChangeEvent); ok {
				c.discarding, c.suggOff = false, false
				c.edited(u.now())
			}
		}
	}
	u.updateSuggestions(gtx)
	for {
		ev, ok := gtx.Event(
			key.Filter{Name: key.NameReturn, Required: key.ModCtrl},
			key.Filter{Name: key.NameEnter, Required: key.ModCtrl},
			key.Filter{Name: "E", Required: key.ModCtrl},
			key.Filter{Name: "S", Required: key.ModCtrl},
			key.Filter{Name: "O", Required: key.ModCtrl},
			key.Filter{Name: "R", Required: key.ModCtrl},
			key.Filter{Name: key.NameEscape},
		)
		if !ok {
			break
		}
		ke, ok := ev.(key.Event)
		if !ok || ke.State != key.Press {
			continue
		}
		if c.busy {
			continue
		}
		if ke.Name != key.NameEscape {
			c.discarding = false
		}
		switch ke.Name {
		case key.NameReturn, key.NameEnter:
			u.sendComposed()
		case "S":
			u.saveDraft()
		case "E":
			u.editExternally()
		case "O":
			u.pickFiles()
		case "R":
			if n := len(c.attachments); n > 0 {
				u.notice = "Removed " + c.attachments[n-1].Name
				c.attachments = c.attachments[:n-1]
				c.edited(u.now())
			}
		case key.NameEscape:
			if !c.changed() || c.discarding {
				u.notice = ""
				u.leaveCompose()
				return
			}
			c.discarding = true
			u.notice = "Esc again discards this message · Ctrl+S saves it to Drafts"
		}
	}
}

func (u *ui) layoutCompose(gtx layout.Context) layout.Dimensions {
	c := u.comp
	hints := composeHints
	if len(c.sugg) > 0 {
		hints = "↑/↓ choose an address · Tab or Enter fills it in · Esc closes the list · Ctrl+Enter send"
	}
	title := "New message"
	switch {
	case c.base.InReplyTo != "":
		title = "Reply"
	case len(c.base.Attachments) > 0 || strings.HasPrefix(strings.ToLower(c.base.Subject), "fwd:"):
		title = "Forward"
	}
	header := func(gtx layout.Context) layout.Dimensions {
		return layout.Inset{Top: 10, Bottom: 6, Left: 12, Right: 12}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			l := material.Label(u.th, 16, title+" · "+u.account.Name)
			l.Font.Weight = font.Bold
			return l.Layout(gtx)
		})
	}
	rows := []layout.FlexChild{
		layout.Rigid(header),
		layout.Rigid(u.field("From", &c.from, "")),
		layout.Rigid(u.field("To", &c.to, "name@example.com, …")),
		layout.Rigid(u.suggestionList(&c.to)),
		layout.Rigid(u.field("Cc", &c.cc, "")),
		layout.Rigid(u.suggestionList(&c.cc)),
		layout.Rigid(u.field("Bcc", &c.bcc, "")),
		layout.Rigid(u.suggestionList(&c.bcc)),
		layout.Rigid(u.field("Subject", &c.subject, "")),
		layout.Rigid(u.divider),
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			return layout.UniformInset(12).Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				gtx.Constraints.Min = gtx.Constraints.Max
				size := gtx.Constraints.Max
				defer clip.Rect{Max: size}.Push(gtx.Ops).Pop()
				ed := material.Editor(u.th, &c.body, "Write your message…")
				ed.Font = u.mono
				return ed.Layout(gtx)
			})
		}),
		layout.Rigid(u.divider),
		layout.Rigid(u.attachmentRow),
		layout.Rigid(u.statusBar(hints)),
	}
	u.autosaveDue(gtx)
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx, rows...)
}

func (u *ui) field(label string, ed *widget.Editor, hint string) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		inset := layout.Inset{Top: 4, Bottom: 4, Left: 12, Right: 12}
		return inset.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Alignment: layout.Baseline}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					gtx.Constraints.Min.X = gtx.Dp(76)
					l := material.Label(u.th, u.th.TextSize, label+":")
					l.Color = u.muted
					return l.Layout(gtx)
				}),
				layout.Flexed(1, material.Editor(u.th, ed, hint).Layout),
			)
		})
	}
}

func (u *ui) attachmentRow(gtx layout.Context) layout.Dimensions {
	c := u.comp
	text := "No attachments · Ctrl+O to add, or Attach: lines in the external editor"
	col := u.muted
	if len(c.attachments) > 0 {
		var parts []string
		for _, a := range c.attachments {
			size := "missing"
			if n := a.Size(); n >= 0 {
				size = humanSize(int(n))
			}
			parts = append(parts, fmt.Sprintf("%s (%s)", a.Name, size))
		}
		text, col = "Attachments: "+strings.Join(parts, " · "), u.th.Fg
	}
	return layout.Inset{Top: 8, Bottom: 8, Left: 12, Right: 12}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		l := material.Label(u.th, u.th.TextSize*0.9, text)
		l.Color, l.MaxLines = col, 2
		return l.Layout(gtx)
	})
}

// saveDraft stores the message in Drafts and closes it, like mutt's
// postpone. A draft being continued is replaced: the new copy is saved
// first, then the old one deleted, so a failure never loses it.
func (u *ui) saveDraft() {
	c := u.comp
	if u.daemon == nil {
		u.notice = "foonmaild isn't running: can't save to Drafts"
		return
	}
	raw, err := compose.BuildDraft(c.draft())
	if err != nil {
		u.notice = "Can't save: " + err.Error()
		return
	}
	c.busy = true
	u.notice = "Saving to Drafts…"
	account, old := u.account.Name, c.draftOf
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		err := u.daemon.SaveDraft(ctx, account, raw)
		u.post(func() {
			c.busy = false
			switch {
			case errors.Is(err, rpc.ErrNotFound):
				u.notice = "Couldn't save: this account has no Drafts folder"
			case err != nil:
				u.notice = "Couldn't save to Drafts: " + err.Error()
			default:
				if old != nil {
					u.deleteDraft(*old)
				}
				if u.comp == c {
					u.leaveCompose()
				}
				u.notice = "Saved to Drafts"
			}
		})
	}()
}

// continueDraft opens a stored draft in compose.
func (u *ui) continueDraft(e cache.Entry) {
	u.notice = "Opening the draft…"
	go func() {
		raw, err := u.loadRaw(e.Summary)
		var d *compose.Draft
		if err == nil {
			d, err = compose.FromMessage(raw)
		}
		u.post(func() {
			if err != nil {
				u.notice = "Couldn't open the draft: " + err.Error()
				return
			}
			u.notice = ""
			if d.From == "" {
				d.From = compose.New(u.account).From
			}
			u.startCompose(d, nil)
			u.comp.draftOf = &e
		})
	}()
}

// deleteDraft removes a stored draft that was sent or saved again.
func (u *ui) deleteDraft(e cache.Entry) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := u.daemon.DeleteMessages(ctx, e.FolderID, []uint32{e.UID}); err != nil {
			u.post(func() { u.notice = "The old draft is still in Drafts: " + err.Error() })
		}
	}()
}

// inDrafts reports whether the current folder is the Drafts folder.
func (u *ui) inDrafts() bool { return u.folder.SpecialUse == `\Drafts` }

// openSelected opens the selected conversation, or in Drafts continues
// the draft.
func (u *ui) openSelected() {
	c := u.current()
	if c == nil {
		return
	}
	if u.inDrafts() {
		u.continueDraft(c.members[len(c.members)-1])
		return
	}
	u.open()
}
