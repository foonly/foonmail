package main

import (
	"gioui.org/font"
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"
)

// ? (or F1, which also works while writing) shows every key, grouped by
// where it works, with more words than the hints at the bottom of each
// view. It opens at the section for where it was pressed.

type helpSection struct {
	title string
	intro string
	keys  [][2]string // key, what it does
}

var helpSections = []helpSection{
	{
		title: "Everywhere",
		keys: [][2]string{
			{"?", "This help. While writing, where ? is typed, use F1."},
			{"F1", "This help, from anywhere."},
			{"Ctrl+Q", "Quit foonmail. The daemon keeps syncing and notifying in the background."},
		},
	},
	{
		title: "Message list",
		intro: "The list shows conversations: a thread's messages in this folder, with your replies from Sent.",
		keys: [][2]string{
			{"j / k, ↓ / ↑", "Move to the next or previous conversation."},
			{"Page Down / Page Up", "Move a screenful at a time."},
			{"Home or = / End or *", "Jump to the first or the last conversation."},
			{"Enter", "Open the conversation. Its unread messages are marked read."},
			{"c or ←", "Open the folder pane to switch folders (see Folder pane)."},
			{"Tab / Shift+Tab", "Show the next or previous account's inbox."},
			{"m", "Write a new message."},
			{"r", "Reply to all: the sender, and everyone else in To and Cc except you. g does the same."},
			{"Shift+R", "Reply to the sender only."},
			{"f", "Forward the newest message."},
			{"a", "Archive: move the conversation to the archive folder (on Gmail, All Mail)."},
			{"s", "Junk it: move it to Junk, so the server's spam filter learns from it. foonmail then shows other conversations that look like the same spam (see Look-alikes)."},
			{"d or Delete", "Move it to Trash. In Trash, a second d deletes it for good."},
			{"v", "Move it to a folder you choose in the folder pane."},
			{"N", "Mark it read, or unread again."},
			{"!", "Flag it, or remove the flag. Other mail clients show the flag as a star or a flag."},
			{"u", "Show only unread conversations. Ones you read while it's on stay until you press u or Esc."},
			{"Shift+F", "Mail filters: rules the server applies to arriving mail (see Mail filters)."},
			{"q", "Quit."},
		},
	},
	{
		title: "Marking several conversations",
		intro: "Marked conversations are acted on together by a, s, d, v, N and !.",
		keys: [][2]string{
			{"Space", "Mark or unmark the selected conversation and move to the next."},
			{"Shift+j / Shift+k", "Mark the conversations you move over."},
			{"Ctrl+click, Shift+click", "Mark one conversation, or everything up to the one clicked."},
			{"Esc", "Clear the marks."},
		},
	},
	{
		title: "Filtering and searching",
		keys: [][2]string{
			{"/", "Filter the list as you type: sender, recipients and subject of the cached mail in this folder."},
			{"Enter", "Done typing: back to the list, still filtered."},
			{"Esc", "Clear the filter."},
			{"Ctrl+Enter", "Search the server instead, for mail that isn't cached: every folder but Trash and Junk (on Gmail, All Mail). From, To and Subject are searched first, then the whole message, so results keep arriving (see Server search results)."},
		},
	},
	{
		title: "Server search results",
		intro: "The results replace the list while they last. The header shows which folder is being searched and names any folder that didn't answer in time.",
		keys: [][2]string{
			{"j / k", "Move between results. Each shows its folder, and \"in body\" when only the message text matched."},
			{"Enter", "Open the result in its own folder, where all the usual keys work. Closing it brings you back to the results."},
			{"/", "Change the search."},
			{"Esc", "Stop searching and go back to the list and filter as they were."},
		},
	},
	{
		title: "Look-alikes after junking",
		intro: "After s, the list narrows to conversations in the folder that look like the junked one: the same sender with mostly the same subject, or any sender with nearly the same subject of three words or more. They are all marked.",
		keys: [][2]string{
			{"s", "Junk all the marked look-alikes too."},
			{"d", "Move them to Trash instead."},
			{"Space", "Unmark one that isn't spam."},
			{"Esc", "Back to the whole list, where you were."},
		},
	},
	{
		title: "Folder pane",
		intro: "Folders of every account, with Drafts, Sent, Archive, Junk and Trash first. Moving through them shows each one's mail.",
		keys: [][2]string{
			{"j / k", "Preview the next or previous folder."},
			{"Enter or →", "Open the folder and sync it with the server. When moving mail (v), move it here."},
			{"n", "Create a folder in the account shown. Use / for a subfolder, as in Clients/Acme."},
			{"d, d", "Delete the folder and all its mail on the server. Not INBOX or the special folders."},
			{"Esc, c or ←", "Back to the folder you came from."},
		},
	},
	{
		title: "Reading a conversation",
		intro: "Messages that were unread when you opened the conversation are tinted and marked ● and new, even after they are marked read. The current message has a bar down its left side. When you were added to a conversation late, the messages before it are read back from the quote in the reply and shown above it, muted with a dashed bar: they are not real messages, and keys used on them act on the reply.",
		keys: [][2]string{
			{"n / p", "Next or previous message in the conversation."},
			{"↓ / ↑", "Scroll through a long message, then go on to the next or previous one."},
			{"Space / -", "Page down or up."},
			{"j / k, → / ←", "Next or previous conversation in the list."},
			{"h", "Show or hide quoted text."},
			{"number, Enter", "Open link number so-and-so in the browser. The numbers are shown after each link."},
			{"o / w", "Open an attachment, or save it to your downloads folder. Type its number first if there are several. Programs and scripts are only saved, never opened."},
			{"b", "Show the message's HTML in your browser, with remote images and scripts blocked."},
			{"r", "Reply to all: the sender, and everyone else in To and Cc except you. g does the same."},
			{"Shift+R", "Reply to the sender only."},
			{"f", "Forward the message you're on."},
			{"a / s / d / v", "Archive, junk, trash or move the conversation, as in the list."},
			{"!", "Flag or unflag the conversation."},
			{"m", "Write a new message."},
			{"Esc or q", "Back to the list."},
		},
	},
	{
		title: "Writing",
		keys: [][2]string{
			{"Tab", "Move between the fields."},
			{"↑ / ↓, Tab or Enter", "Choose and fill in a suggested address."},
			{"Ctrl+Enter", "Send. It waits a few seconds first, so it can be undone from the notification."},
			{"Ctrl+S", "Save to Drafts and close."},
			{"Ctrl+E", "Edit the message in your text editor ($EDITOR)."},
			{"Ctrl+O", "Attach files."},
			{"Ctrl+R", "Remove the last attachment."},
			{"Esc, Esc", "Discard the message. What you write is also kept on disk as you type, so it comes back after a crash."},
		},
	},
	{
		title: "Mail filters",
		intro: "Rules the server applies to mail as it arrives, so they work whichever mail client is open. The first matching rule files the message; the server's spam rule always runs first.",
		keys: [][2]string{
			{"j / k", "Choose a rule."},
			{"n", "New rule, filled in from the message you pressed Shift+F on."},
			{"Enter", "Edit the rule."},
			{"d, d", "Delete the rule."},
			{"Shift+j / Shift+k", "Move the rule down or up."},
			{"e", "Edit the whole Sieve script in your text editor. The server checks it before saving."},
			{"y", "After saving a rule made from a message: also move the matching mail already in the folder."},
			{"Esc", "Back."},
		},
	},
	{
		title: "Writing a filter rule",
		keys: [][2]string{
			{"Tab", "Move between the parts of the rule."},
			{"← / →", "On Match: choose what to match: the sender, the sender's domain, a mailing list, a To or Cc address (for aliases), or words in the subject."},
			{"↑ / ↓, Enter", "In Folder: choose one of the folders matching what you typed. A new folder is created when the first message arrives."},
			{"Space", "Tick or untick \"also mark it read\"."},
			{"Enter", "Save the rule."},
			{"Esc", "Cancel."},
		},
	},
}

// helpView is the help screen.
type helpView struct {
	returnTo mode
	tag      *int
	list     widget.List
}

// helpStart is the section to open the help at, for where it was opened.
func (u *ui) helpStart() string {
	switch {
	case u.mode == modeFilters && u.filters != nil && u.filters.form != nil:
		return "Writing a filter rule"
	case u.mode == modeFilters:
		return "Mail filters"
	case u.mode == modeView:
		return "Reading a conversation"
	case u.mode == modeCompose:
		return "Writing"
	case u.pane.open:
		return "Folder pane"
	case u.showingResults():
		return "Server search results"
	case u.similar != nil:
		return "Look-alikes after junking"
	}
	return "Message list"
}

func (u *ui) openHelp() {
	if u.mode == modeHelp {
		return
	}
	h := &helpView{returnTo: u.mode, tag: new(int)}
	h.list.Axis = layout.Vertical
	start := u.helpStart()
	for i, s := range helpSections {
		if s.title == start {
			h.list.Position.First = i
		}
	}
	u.help = h
	u.mode = modeHelp
	u.focusReq = h.tag
}

func (u *ui) closeHelp() {
	h := u.help
	u.help = nil
	u.mode = h.returnTo
	switch {
	case u.mode == modeList && u.pane.open:
		u.focusReq = u.pane.tag
	case u.mode == modeList:
		u.focusReq = u.listTag
	case u.mode == modeFilters && u.filters != nil:
		u.focusReq = u.filters.tag
	}
}

func (u *ui) updateHelp(gtx layout.Context) {
	h := u.help
	t := h.tag
	for {
		ev, ok := gtx.Event(
			key.FocusFilter{Target: t},
			key.Filter{Focus: t, Name: "J"},
			key.Filter{Focus: t, Name: "K"},
			key.Filter{Focus: t, Name: key.NameDownArrow},
			key.Filter{Focus: t, Name: key.NameUpArrow},
			key.Filter{Focus: t, Name: key.NameSpace},
			key.Filter{Focus: t, Name: key.NamePageDown},
			key.Filter{Focus: t, Name: "-"},
			key.Filter{Focus: t, Name: key.NamePageUp},
			key.Filter{Focus: t, Name: key.NameHome},
			key.Filter{Focus: t, Name: key.NameEnd},
			key.Filter{Focus: t, Name: key.NameEscape},
			key.Filter{Focus: t, Name: "Q"},
			key.Filter{Focus: t, Name: "?", Optional: key.ModShift},
		)
		if !ok {
			break
		}
		ke, ok := ev.(key.Event)
		if !ok || ke.State != key.Press {
			continue
		}
		page := max(u.viewH*8/10, gtx.Dp(40))
		p := &h.list.Position
		switch ke.Name {
		case "J", key.NameDownArrow:
			p.Offset += gtx.Dp(40)
		case "K", key.NameUpArrow:
			p.Offset -= gtx.Dp(40)
		case key.NameSpace, key.NamePageDown:
			p.Offset += page
		case "-", key.NamePageUp:
			p.Offset -= page
		case key.NameHome:
			*p = layout.Position{}
		case key.NameEnd:
			p.First, p.Offset = len(helpSections), 0
		case key.NameEscape, "Q", "?":
			u.closeHelp()
			return
		}
	}
}

func (u *ui) layoutHelp(gtx layout.Context) layout.Dimensions {
	h := u.help
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Top: 10, Bottom: 10, Left: 12, Right: 12}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				l := material.Label(u.th, 16, "Keys")
				l.Font.Weight = font.Bold
				return l.Layout(gtx)
			})
		}),
		layout.Rigid(u.divider),
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			size := gtx.Constraints.Max
			u.viewH = size.Y
			defer clip.Rect{Max: size}.Push(gtx.Ops).Pop()
			event.Op(gtx.Ops, h.tag)
			material.List(u.th, &h.list).Layout(gtx, len(helpSections), func(gtx layout.Context, i int) layout.Dimensions {
				return u.helpSection(gtx, helpSections[i])
			})
			return layout.Dimensions{Size: size}
		}),
		layout.Rigid(u.statusBar("j/k or ↑/↓ scroll · Space/- page · Home/End · Esc, q or ? close")),
	)
}

func (u *ui) helpSection(gtx layout.Context, s helpSection) layout.Dimensions {
	var rows []layout.FlexChild
	rows = append(rows, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
		l := material.Label(u.th, u.th.TextSize*1.1, s.title)
		l.Font.Weight = font.Bold
		l.Color = u.accent
		return layout.Inset{Top: 14, Bottom: 6}.Layout(gtx, l.Layout)
	}))
	if s.intro != "" {
		rows = append(rows, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			l := material.Label(u.th, u.th.TextSize*0.95, s.intro)
			l.Color = u.muted
			return layout.Inset{Bottom: 6}.Layout(gtx, l.Layout)
		}))
	}
	for _, k := range s.keys {
		rows = append(rows, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Top: 3, Bottom: 3}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{}.Layout(gtx,
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						w := gtx.Dp(unit.Dp(190))
						gtx.Constraints.Min.X, gtx.Constraints.Max.X = w, w
						l := material.Label(u.th, u.th.TextSize, k[0])
						l.Font.Weight = font.Bold
						return layout.Inset{Right: 12}.Layout(gtx, l.Layout)
					}),
					layout.Flexed(1, material.Label(u.th, u.th.TextSize, k[1]).Layout),
				)
			})
		}))
	}
	return layout.Inset{Left: 16, Right: 16}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx, rows...)
	})
}
