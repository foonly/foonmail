// Command compose-proto is a throwaway prototype of the foonmail compose view.
// It exists to test Gio's text handling on Wayland before committing to it:
// typing (ä/ö, dead keys, compose sequences), clipboard, undo, keyboard focus,
// the portal file picker, and handing the draft to an external editor.
//
// Nothing is sent. Ctrl+Enter builds the MIME message and prints it to stdout.
package main

import (
	"errors"
	"fmt"
	"image"
	"image/color"
	"log"
	"os"
	"strings"
	"time"

	"gioui.org/app"
	"gioui.org/font"
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/system"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"
	"gioui.org/x/explorer"

	"foonmail/internal/desktop"
)

const keyHints = "Ctrl+Enter send (prints to stdout) · Ctrl+E external editor · Ctrl+O attach · " +
	"Ctrl+R remove last attachment · Tab next field · Ctrl+Q quit"

type attachment struct {
	path string
	size int64
}

type ui struct {
	w    *app.Window
	th   *material.Theme
	expl *explorer.Explorer

	to, cc, subject, body widget.Editor

	attachments []attachment
	status      string
	busy        bool // external editor or file picker is open
	focused     bool // initial focus has been set

	results chan func() // UI updates posted from background goroutines

	mono               font.Font
	muted, line, barBg color.NRGBA
}

func main() {
	app.ID = "foonmail-compose" // Wayland app_id, for Hyprland window rules
	go func() {
		w := new(app.Window)
		w.Option(app.Title("foonmail compose prototype"), app.Size(unit.Dp(900), unit.Dp(650)))
		if err := run(w); err != nil {
			log.Fatal(err)
		}
		os.Exit(0)
	}()
	app.Main()
}

func run(w *app.Window) error {
	u := newUI(w)
	var ops op.Ops
	for {
		e := w.Event()
		u.expl.ListenEvents(e)
		switch e := e.(type) {
		case app.DestroyEvent:
			return e.Err
		case app.FrameEvent:
			gtx := app.NewContext(&ops, e)
			u.drain()
			u.handleKeys(gtx)
			u.layout(gtx)
			e.Frame(gtx.Ops)
		}
	}
}

func newUI(w *app.Window) *ui {
	u := &ui{
		w:       w,
		th:      material.NewTheme(),
		expl:    explorer.NewExplorer(w),
		results: make(chan func(), 16),
		mono:    font.Font{Typeface: "monospace"},
	}
	if desktop.PrefersDark() {
		u.th.Palette = material.Palette{
			Bg: rgb(0x1d1f21), Fg: rgb(0xe0e0e0),
			ContrastBg: rgb(0x6ea8fe), ContrastFg: rgb(0x1d1f21),
		}
		u.muted, u.line, u.barBg = rgb(0x9a9a9a), rgb(0x3a3d40), rgb(0x25282b)
	} else {
		u.th.Palette = material.Palette{
			Bg: rgb(0xfafafa), Fg: rgb(0x1f1f1f),
			ContrastBg: rgb(0x2f6fde), ContrastFg: rgb(0xffffff),
		}
		u.muted, u.line, u.barBg = rgb(0x6b6b6b), rgb(0xd8d8d8), rgb(0xf0f0f0)
	}

	u.to.SingleLine, u.cc.SingleLine, u.subject.SingleLine = true, true, true
	u.to.SetText("Bob Example <bob@example.com>")
	u.subject.SetText("Re: Kahvitauko torstaina?")
	u.body.SetText(sampleReply)
	u.body.SetCaret(0, 0)
	return u
}

// post schedules f to run on the UI goroutine and wakes the window.
func (u *ui) post(f func()) {
	u.results <- f
	u.w.Invalidate()
}

func (u *ui) drain() {
	for {
		select {
		case f := <-u.results:
			f()
		default:
			return
		}
	}
}

func (u *ui) handleKeys(gtx layout.Context) {
	filters := []event.Filter{
		key.Filter{Name: key.NameReturn, Required: key.ModCtrl},
		key.Filter{Name: key.NameEnter, Required: key.ModCtrl},
		key.Filter{Name: "E", Required: key.ModCtrl},
		key.Filter{Name: "O", Required: key.ModCtrl},
		key.Filter{Name: "R", Required: key.ModCtrl},
		key.Filter{Name: "Q", Required: key.ModCtrl},
	}
	for {
		ev, ok := gtx.Event(filters...)
		if !ok {
			return
		}
		ke, ok := ev.(key.Event)
		if !ok || ke.State != key.Press {
			continue
		}
		if ke.Name == "Q" {
			u.w.Perform(system.ActionClose)
			continue
		}
		if u.busy {
			u.status = "Busy: finish in the external editor or file picker first."
			continue
		}
		switch ke.Name {
		case key.NameReturn, key.NameEnter:
			u.send()
		case "E":
			u.openExternalEditor()
		case "O":
			u.pickFiles()
		case "R":
			u.removeLastAttachment()
		}
	}
}

func (u *ui) currentDraft() draft {
	d := draft{To: u.to.Text(), Cc: u.cc.Text(), Subject: u.subject.Text(), Body: u.body.Text()}
	for _, a := range u.attachments {
		d.Attach = append(d.Attach, a.path)
	}
	return d
}

func (u *ui) applyDraft(d draft) {
	u.to.SetText(d.To)
	u.cc.SetText(d.Cc)
	u.subject.SetText(d.Subject)
	u.body.SetText(d.Body)
	u.attachments = nil
	for _, p := range d.Attach {
		u.addAttachment(p)
	}
}

func (u *ui) send() {
	d := u.currentDraft()
	go func() {
		msg, err := buildMessage(fromAddress(), d, time.Now())
		if err == nil {
			fmt.Printf("%s\n----- end of message (%d bytes) -----\n", msg, len(msg))
		}
		u.post(func() {
			if err != nil {
				u.status = "Can't build message: " + err.Error()
				return
			}
			u.status = fmt.Sprintf("Built a %s message and printed it to stdout. Nothing was sent.", humanSize(int64(len(msg))))
		})
	}()
}

func (u *ui) openExternalEditor() {
	argv := strings.Fields(os.Getenv("FOONMAIL_EDITOR"))
	if len(argv) == 0 {
		argv = []string{"foot", "micro"}
	}
	d := u.currentDraft()
	u.busy = true
	u.status = "Editing in " + strings.Join(argv, " ") + "…"
	go func() {
		nd, outcome, err := editExternally(argv, d)
		u.post(func() {
			u.busy = false
			switch {
			case err != nil:
				u.status = "External editor failed: " + err.Error()
			case outcome == editUnchanged:
				u.status = "External editor closed without changes."
			case outcome == editCancelled:
				u.status = "External edit abandoned; the draft here is unchanged."
			default:
				u.applyDraft(nd)
				u.status = "Draft updated from the external editor."
			}
		})
	}()
}

func (u *ui) pickFiles() {
	u.busy = true
	u.status = "Choosing files…"
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
			u.busy = false
			switch {
			case errors.Is(err, explorer.ErrUserDecline):
				u.status = "No file chosen."
			case err != nil:
				u.status = "File picker failed: " + err.Error()
			default:
				for _, p := range paths {
					u.addAttachment(p)
				}
				u.status = fmt.Sprintf("Attached %d file(s).", len(paths))
			}
		})
	}()
}

func (u *ui) addAttachment(path string) {
	for _, a := range u.attachments {
		if a.path == path {
			return
		}
	}
	var size int64
	if fi, err := os.Stat(path); err == nil {
		size = fi.Size()
	}
	u.attachments = append(u.attachments, attachment{path: path, size: size})
}

func (u *ui) removeLastAttachment() {
	if len(u.attachments) == 0 {
		u.status = "No attachments to remove."
		return
	}
	last := u.attachments[len(u.attachments)-1]
	u.attachments = u.attachments[:len(u.attachments)-1]
	u.status = "Removed " + baseName(last.path) + "."
}

// Layout

func (u *ui) layout(gtx layout.Context) layout.Dimensions {
	paint.Fill(gtx.Ops, u.th.Bg)
	for _, ed := range []*widget.Editor{&u.to, &u.cc, &u.subject, &u.body} {
		ed.ReadOnly = u.busy
	}
	dims := layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(u.headerRow("To", &u.to, "name@example.com, …")),
		layout.Rigid(u.headerRow("Cc", &u.cc, "")),
		layout.Rigid(u.headerRow("Subject", &u.subject, "")),
		layout.Rigid(u.divider),
		layout.Flexed(1, u.bodyArea),
		layout.Rigid(u.divider),
		layout.Rigid(u.attachmentsRow),
		layout.Rigid(u.statusBar),
	)
	if !u.focused {
		gtx.Execute(key.FocusCmd{Tag: &u.body})
		u.focused = true
	}
	return dims
}

func (u *ui) headerRow(label string, ed *widget.Editor, hint string) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		inset := layout.Inset{Top: unit.Dp(6), Bottom: unit.Dp(6), Left: unit.Dp(12), Right: unit.Dp(12)}
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

func (u *ui) bodyArea(gtx layout.Context) layout.Dimensions {
	return layout.UniformInset(unit.Dp(12)).Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min = gtx.Constraints.Max
		ed := material.Editor(u.th, &u.body, "Write your message…")
		ed.Font = u.mono
		return ed.Layout(gtx)
	})
}

func (u *ui) divider(gtx layout.Context) layout.Dimensions {
	size := image.Pt(gtx.Constraints.Max.X, gtx.Dp(1))
	paint.FillShape(gtx.Ops, u.line, clip.Rect{Max: size}.Op())
	return layout.Dimensions{Size: size}
}

func (u *ui) attachmentsRow(gtx layout.Context) layout.Dimensions {
	text := "No attachments. Ctrl+O to add, or Attach: lines in the external editor."
	col := u.muted
	if len(u.attachments) > 0 {
		var parts []string
		var total int64
		for _, a := range u.attachments {
			parts = append(parts, fmt.Sprintf("%s (%s)", baseName(a.path), humanSize(a.size)))
			total += a.size
		}
		text = fmt.Sprintf("Attachments, %s total: %s", humanSize(total), strings.Join(parts, " · "))
		col = u.th.Fg
	}
	return layout.UniformInset(unit.Dp(12)).Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		l := material.Label(u.th, u.th.TextSize*0.9, text)
		l.Color = col
		return l.Layout(gtx)
	})
}

func (u *ui) statusBar(gtx layout.Context) layout.Dimensions {
	macro := op.Record(gtx.Ops)
	dims := layout.Inset{Top: unit.Dp(8), Bottom: unit.Dp(8), Left: unit.Dp(12), Right: unit.Dp(12)}.Layout(gtx,
		func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					if u.status == "" {
						return layout.Dimensions{}
					}
					return layout.Inset{Bottom: unit.Dp(4)}.Layout(gtx,
						material.Label(u.th, u.th.TextSize*0.9, u.status).Layout)
				}),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					l := material.Label(u.th, u.th.TextSize*0.8, keyHints)
					l.Color = u.muted
					return l.Layout(gtx)
				}),
			)
		})
	call := macro.Stop()
	paint.FillShape(gtx.Ops, u.barBg, clip.Rect{Max: dims.Size}.Op())
	call.Add(gtx.Ops)
	return dims
}

func rgb(c uint32) color.NRGBA {
	return color.NRGBA{R: uint8(c >> 16), G: uint8(c >> 8), B: uint8(c), A: 0xff}
}

func fromAddress() string {
	if f := os.Getenv("FOONMAIL_FROM"); f != "" {
		return f
	}
	return "Foonmail Prototype <prototype@foonmail.invalid>"
}

const sampleReply = `

On Tue, 3 Nov 2026 at 14:02, Bob Example <bob@example.com> wrote:
> Hei! Käydäänkö torstaina kahvilla? Pöytävaraus on klo 14.
>
> Tässä vielä pitkä rivi, jonka tarkoitus on testata rivitystä ja valintaa: äöå ÄÖÅ, euro €, ajatusviiva — ja lainausmerkit ”näin”. Jatketaan vielä hetki, jotta rivi varmasti ylittää ikkunan leveyden.
>
> Terveisin,
> Bob
`
