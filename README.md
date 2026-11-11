# foonmail

A small mail client for the Linux desktop, written in Go. It works on plain
text: mail is shown as text, HTML mail is converted to readable text, and
what you send is plain text (format=flowed). It's built for keyboard use.

It has two parts:

- **foonmaild** is a background daemon. It keeps an IMAP connection open
  (IDLE) for each account and caches mail in SQLite. It sends through SMTP
  from an outbox with a short undo window. It shows new mail with a tray
  icon and desktop notifications.
- **foonmail** is the window (built with [Gio](https://gioui.org)). It reads
  the cache directly and asks the daemon over D-Bus for anything that needs
  the server. It opens quickly and works offline on whatever is cached.

There is also **foonmailctl**, a command-line tool for the daemon.

## Features

- Several IMAP/SMTP accounts, with passwords kept in the Secret Service
  keyring (GNOME Keyring, KWallet, KeePassXC…), never in the config file.
  The folder pane lists the shown account's folders, with each other
  account on one line with its unread count; Tab switches account. A
  message is sent through the account its From address belongs to.
- Conversations: replies are grouped across folders, so your own answers
  from Sent appear in the thread.
- HTML mail converted to text, with numbered links listed under each
  message. Long tracking addresses are shortened, and the link's real target
  is shown when the visible text claims a different one. When a mail's plain
  text version is just a stub or badly made, the HTML version is used
  instead.
- Color for links, quoted text (by depth), attributions and signatures.
  Quoted text at the end of a reply is folded away until you press h.
- Writing in the window or in your own editor, with drafts, attachments,
  replies, reply-all and forwarding. Sent mail waits 5 seconds before it
  goes, so you can take it back (`undo_delay` in the config changes that).
- Address suggestions in To, Cc and Bcc from the people you exchange mail
  with, those you write to most and most recently first.
- A signature per account, added to new messages, replies and forwards.
- What you are writing is kept on disk as you type, and comes back if
  foonmail is closed or crashes before you send or save it.
- Attachments opened with your desktop's default app or saved to your
  download folder. Programs, scripts and web pages are only saved, never
  opened.
- A mail's HTML version shown in your browser when the text isn't enough,
  with scripts and remote images blocked.
- Archive, junk, trash or move to any folder with one key, for one
  conversation or several marked ones. A folder pane lists all folders,
  with the special ones (Drafts, Sent, Archive, Junk, Trash) first, and
  creates and deletes folders.
- Server search across all folders, message bodies included, for mail
  that isn't in the cache. What it finds is added to the cache.
- Mail filters kept on the server (Sieve, over ManageSieve), so they work
  whichever client is open: file mail from a sender, a domain, a mailing
  list, to an alias or with a subject into a folder, optionally marked
  read. Shift+F on a message starts a filter like it and offers to move the
  matching mail already there.
- Junking a message brings up the rest of the spam run: the conversations
  in the folder from the same sender (or the same name with a number
  added) with mostly the same subject, or from anyone with nearly the
  same subject, ready to review and junk or trash in one go.
- A tray icon (StatusNotifierItem) that lights up for new mail, with a
  right-click menu (Open foonmail, Sync now), and notifications that open
  the message when clicked.
- Reconnects as soon as the network comes up (through NetworkManager) or
  the machine wakes from sleep, instead of at its next retry.

## Requirements

- Linux with a Wayland or X11 desktop and a D-Bus session bus.
- Go 1.27 or newer to build.
- A Secret Service provider for passwords.
- Optional: a tray host that supports StatusNotifierItem (KDE, waybar,
  Quickshell; GNOME needs an extension), a notification server, and an
  emoji font such as Noto Color Emoji.

## Building and installing

```sh
make build      # bin/foonmaild, bin/foonmail, bin/foonmailctl
make install    # into ~/.local, then enables and starts foonmaild
```

`make install` needs no root. It installs:

- the programs in `~/.local/bin`
- a desktop file
- D-Bus activation files, so a notification click can start the GUI
- a systemd user unit for the daemon, which it enables and (re)starts

`make uninstall` removes all of that again. It leaves your config, cache and
outbox alone. `make help` lists all the targets.

The build uses the `novulkan` tag, so Gio draws with OpenGL ES and the
`vulkan-headers` package isn't needed. If you have it installed, you can
build with `make TAGS=`.

## Configuration

Copy `config.example.toml` to `~/.config/foonmail/config.toml` (or point
`FOONMAIL_CONFIG` at another file) and fill in your account:

```toml
[[account]]
name = "home"
email = "you@example.com"
display_name = "Your Name"

[account.imap]
host = "mail.example.com"
username = "you"
```

SMTP uses the IMAP host and username unless you set them. The example file
lists every setting. Unknown settings are reported as errors, so typos don't
go unnoticed.

Then store the passwords in the keyring:

```sh
foonmailctl passwords
```

It lists the passwords your accounts need and which the keyring has, asks
for the missing ones, and logs in to each server before storing what you
typed, so a typo is caught at once. `-check` also logs in with the stored
passwords, and `-change ACCOUNT` replaces one. (Passwords are stored under
service `foonmail/HOST` and your login, should you want `secret-tool`.)

### Gmail

Gmail works with an app password: turn on 2-Step Verification, create one
at <https://myaccount.google.com/apppasswords>, and give it to
`foonmailctl passwords`. It is asked for once: SMTP uses the IMAP entry.

`config.example.toml` has the account settings. foonmail doesn't copy sent
mail to Sent Mail, since Gmail does that itself, and archiving (a) moves
mail to All Mail, which takes it out of the Inbox.

The cache lives in `~/.cache/foonmail` and can be deleted; it is rebuilt
from the server. The outbox lives in `~/.local/share/foonmail`.

## Using it

Start foonmail from your launcher, or from the tray icon or a notification.
The bottom line of the window always shows the keys for what you're doing,
and ? (F1 while writing) lists every key, with what it does.

| In the message list | |
|---|---|
| j / k | move |
| Enter | open the conversation |
| c or ← | folders |
| Tab / Shift+Tab | next / previous account |
| m | new message |
| r / g / f | reply / reply all / forward |
| a / s / d | archive / junk / trash |
| v | move to a folder (the folder pane opens to choose one) |
| Shift+j/k, Space | mark a range / mark one; a, s, d, v, N and ! then act on all marked (Esc clears) |
| N | mark read or unread |
| ! | flag or unflag (shown as ! in the list; other clients call it a star) |
| after s | the list shows the other conversations that look like the junked mail (same sender and mostly the same subject, or any sender with nearly the same subject of three words or more), all marked: s junks them too, d trashes them, Space unmarks one, Esc goes back |
| u | only unread conversations (ones you read stay until u or Esc turns it off) |
| / | filter the cached list as you type |
| Ctrl+Enter in the filter | search the server instead: every folder but Trash and Junk (on Gmail, All Mail), From, To and Subject first, then the whole message. The header shows which folder is being searched; a folder that doesn't answer in time (30 s for headers, 60 s for bodies) is skipped and named there. Results list with their folder, and "in body" for those found by the body alone; Enter opens one in its folder, closing it comes back, / changes the search, Esc goes back to the list |
| Shift+F (list or conversation) | mail filters: the account's rules, applied by the server as mail arrives |

| In the filter view | |
|---|---|
| j / k | choose a rule |
| n | new rule, filled in from the message Shift+F was pressed on |
| Enter | edit the rule |
| d, d | delete it |
| Shift+j / Shift+k | move it down / up: the first matching rule files the message |
| e | edit the whole Sieve script in your editor (the server checks it) |
| y | after a rule made from a message: move the matching mail already in the folder |
| Esc | back |

In the rule form, Tab moves between its parts, ←/→ choose what to match
(sender, domain, mailing list, To or Cc, subject), the folder is completed
from your folders as you type (↑/↓, Enter), Space ticks "also mark it
read", and Enter saves.

| In the folder pane | |
|---|---|
| j / k | preview a folder |
| Enter or → | open and sync it |
| n | new folder in the shown account (/ for a subfolder: Clients/Acme); also while choosing where v moves to |
| d | delete the folder and its mail from the server, after a second d (not INBOX or the special folders) |
| Esc, c or ← | back |

| Reading a conversation | |
|---|---|
| n / p, ↓ / ↑ | next / previous message (arrows scroll through long ones first) |
| j / k, → / ← | next / previous conversation |
| Space / - | page down / up |
| h | show or hide quoted text |
| ! | flag or unflag the conversation |
| number, Enter | open that link |
| o / w | open / save the attachment (type its number first if there are several) |
| b | show the message's HTML in your browser, with remote content blocked |
| Esc | back to the list |

| Writing | |
|---|---|
| ↑ / ↓, Tab or Enter | choose and fill in a suggested address |
| Ctrl+Enter | send |
| Ctrl+S | save to Drafts |
| Ctrl+E | edit in your `$EDITOR` |
| Ctrl+O / Ctrl+R | attach a file / remove the last one |
| Esc twice | discard |

foonmailctl talks to the running daemon:

```sh
foonmailctl status               # accounts: online, unread, new
foonmailctl sync                 # reconnect and resync every account now
foonmailctl send home message.eml
foonmailctl outbox               # what is waiting, sending or sent
foonmailctl undo ID
foonmailctl passwords            # store missing passwords in the keyring
```

The daemon logs to the journal: `journalctl --user -u foonmaild -f`.

## Development

```sh
make run-daemon   # foonmaild in the foreground with debug logging
make run          # the GUI
make check        # go vet and the tests with the race detector
```

The tests use in-memory IMAP and SMTP servers and a private D-Bus bus, so
they never touch your real mail or desktop. A few optional tests read your
own cache. To run them, copy `local.test.env.example` to `local.test.env`
(it is gitignored) and fill it in.

## License

Copyright © 2026 Niklas Schönberg

foonmail is free software: you can redistribute it and/or modify it under
the terms of the GNU General Public License as published by the Free
Software Foundation, either version 3 of the License, or (at your option)
any later version. See [LICENSE](LICENSE).
