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
  replies, reply-all and forwarding. Sent mail waits 10 seconds before it
  goes, so you can take it back.
- Archive, junk and trash with one key. A folder pane lists all folders,
  with the special ones (Drafts, Sent, Archive, Junk, Trash) first.
- A tray icon (StatusNotifierItem) that lights up for new mail, and
  notifications that open the message when clicked.

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

Store each server's password in the keyring:

```sh
secret-tool store --label='foonmail: you on mail.example.com' \
    service foonmail/mail.example.com username you
```

The cache lives in `~/.cache/foonmail` and can be deleted; it is rebuilt
from the server. The outbox lives in `~/.local/share/foonmail`.

## Using it

Start foonmail from your launcher, or from the tray icon or a notification.
The bottom line of the window always shows the keys for what you're doing.

| In the message list | |
|---|---|
| j / k | move |
| Enter | open the conversation |
| c or ← | folders |
| m | new message |
| r / g / f | reply / reply all / forward |
| a / s / d | archive / junk / trash |
| N | mark read or unread |
| / | filter |

| Reading a conversation | |
|---|---|
| n / p, ↓ / ↑ | next / previous message (arrows scroll through long ones first) |
| j / k, → / ← | next / previous conversation |
| Space / - | page down / up |
| h | show or hide quoted text |
| number, Enter | open that link |
| Esc | back to the list |

| Writing | |
|---|---|
| Ctrl+Enter | send |
| Ctrl+S | save to Drafts |
| Ctrl+E | edit in your `$EDITOR` |
| Ctrl+O / Ctrl+R | attach a file / remove the last one |
| Esc twice | discard |

foonmailctl talks to the running daemon:

```sh
foonmailctl status               # accounts: online, unread, new
foonmailctl send home message.eml
foonmailctl outbox               # what is waiting, sending or sent
foonmailctl undo ID
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
