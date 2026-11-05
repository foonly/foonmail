package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"foonmail/internal/imapsync"
	"foonmail/internal/notify"
	"foonmail/internal/outbox"
)

// outboxControl is what the hub needs from the outbox to act on
// notification buttons.
type outboxControl interface {
	Undo(ctx context.Context, id int64) error
	Resolve(ctx context.Context, id int64, resend bool) error
}

// copier stores the outbox's messages through each account's syncer.
type copier map[string]*imapsync.Syncer

func (c copier) AppendSent(ctx context.Context, account string, raw []byte) error {
	s := c[account]
	if s == nil {
		return fmt.Errorf("no account %q", account)
	}
	return s.AppendSent(ctx, raw)
}

func (c copier) AppendDraft(ctx context.Context, account string, raw []byte) error {
	s := c[account]
	if s == nil {
		return fmt.Errorf("no account %q", account)
	}
	return s.AppendDraft(ctx, raw)
}

// notifyOutbox is the outbox's Notify callback.
func (h *hub) notifyOutbox(ev outbox.Event) { h.outboxEvents <- ev }

// undoResult is the outcome of an Undo pressed on a notification.
type undoResult struct {
	id  int64
	err error
}

// handleOutbox shows the sending flow as one notification per message,
// replaced in place as the message moves on.
func (h *hub) handleOutbox(ev outbox.Event) {
	h.log.Info("outbox", "id", ev.ID, "kind", ev.Kind, "err", ev.Err)
	if h.notifier == nil {
		return
	}
	actions := h.notifier.Server().Has("actions")
	n := &notify.Notification{ReplacesID: h.sendNotes[ev.ID], Icon: "mail-send", Category: "email"}
	subject := ev.Subject
	if subject == "" {
		subject = "(no subject)"
	}
	switch ev.Kind {
	case outbox.Queued:
		n.Summary = "Sending to " + ev.To
		n.Body = subject
		n.Urgency = 1
		n.Timeout = h.undoWindow + 5*time.Second // replaced by "Sent" before it expires
		if actions {
			n.Actions = []notify.Action{{Key: "undo", Label: "Undo"}}
			id := ev.ID
			n.OnAction = func(key string) {
				if key == "undo" {
					go h.undo(id)
				}
			}
		}
	case outbox.SentOK:
		n.Summary, n.Body = "Sent to "+ev.To, subject
		n.Timeout = 4 * time.Second
	case outbox.Undone:
		n.Summary, n.Body = "Moved to drafts", subject
		if ev.Err != nil {
			n.Summary = "Not sent"
			n.Body = subject + "\nSaving it to Drafts failed: " + ev.Err.Error()
		}
		n.Timeout = 6 * time.Second
	case outbox.Retrying:
		n.Summary = "Couldn't send yet: will try again"
		n.Body = subject + "\n" + ev.Err.Error()
	case outbox.FailedSend:
		n.Summary = "Couldn't send “" + subject + "”"
		n.Body = ev.Err.Error()
		n.Urgency, n.Timeout = 2, -1
	case outbox.UncertainSend:
		n.Summary = "“" + subject + "” may or may not have been sent"
		n.Body = "The connection broke after the message was handed over. Check Sent or ask " + ev.To +
			"; foonmail won't send it again unless you say so."
		n.Urgency, n.Timeout = 2, -1
		if actions {
			n.Actions = []notify.Action{{Key: "resend", Label: "Send again"}, {Key: "sent", Label: "It was sent"}}
			id := ev.ID
			n.OnAction = func(key string) {
				switch key {
				case "resend":
					go h.resolve(id, true)
				case "sent":
					go h.resolve(id, false)
				}
			}
		}
	default:
		return
	}
	n.AppName = "foonmail"
	id, err := h.notifier.Send(n)
	if err != nil {
		h.log.Warn("notification failed", "err", err)
		return
	}
	h.sendNotes[ev.ID] = id
	switch ev.Kind {
	case outbox.SentOK, outbox.Undone:
		delete(h.sendNotes, ev.ID) // finished: the next notification starts fresh
	}
}

// undo runs an Undo pressed on a notification. Success shows through the
// outbox's Undone event; being too late is reported here.
func (h *hub) undo(id int64) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if h.outbox == nil {
		return
	}
	if err := h.outbox.Undo(ctx, id); err != nil {
		h.undoResults <- undoResult{id: id, err: err}
	}
}

func (h *hub) resolve(id int64, resend bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if h.outbox == nil {
		return
	}
	if err := h.outbox.Resolve(ctx, id, resend); err != nil {
		h.log.Warn("resolving an uncertain message", "id", id, "err", err)
	}
}

func (h *hub) handleUndoResult(r undoResult) {
	if h.notifier == nil {
		return
	}
	n := &notify.Notification{AppName: "foonmail", ReplacesID: h.sendNotes[r.id], Icon: "mail-send",
		Summary: "Couldn't undo", Body: r.err.Error(), Timeout: 6 * time.Second}
	if errors.Is(r.err, outbox.ErrTooLate) {
		n.Summary, n.Body = "Too late to undo", "The message was already being sent."
	}
	if id, err := h.notifier.Send(n); err == nil {
		h.sendNotes[r.id] = id
	}
}
