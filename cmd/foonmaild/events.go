package main

import (
	"log/slog"
	"slices"
	"strings"

	"foonmail/internal/config"
	"foonmail/internal/imapsync"
)

// newEventHandler returns the sync event handler for an account. For now it
// logs what the tray and notifications will show.
func newEventHandler(acct *config.Account, log *slog.Logger) func(imapsync.Event) {
	log = log.With("account", acct.Name)
	own := acct.Addresses()
	return func(ev imapsync.Event) {
		switch ev := ev.(type) {
		case imapsync.NewMail:
			notify, reason := shouldNotify(own, ev)
			log.Info("new mail", "from", ev.Envelope.From.String(), "subject", ev.Envelope.Subject,
				"notify", notify, "reason", reason)
		case imapsync.UnreadChanged:
			log.Info("unread", "count", ev.Unread)
		case imapsync.ConnectionChanged:
			if ev.Online {
				log.Info("online")
			} else {
				log.Warn("offline", "err", ev.Err)
			}
		}
	}
}

// shouldNotify applies the design's rules for new-mail notifications:
// skip mail already read elsewhere (such as on the phone) and mail from the
// user's own addresses. own must be lowercased.
func shouldNotify(own []string, ev imapsync.NewMail) (bool, string) {
	if ev.Seen {
		return false, "already read"
	}
	if slices.Contains(own, strings.ToLower(ev.Envelope.From.Addr)) {
		return false, "from you"
	}
	return true, ""
}
