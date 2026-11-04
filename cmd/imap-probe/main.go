// Command imap-probe connects to an IMAP server the way the foonmail daemon
// will, and prints what it learns:
//
//   - capabilities before and after login (CONDSTORE, QRESYNC, SPECIAL-USE, …)
//   - folders with their special-use flags and message counts
//   - INBOX state, then live changes while in IDLE: new mail, flag changes
//     (such as reading a message on the phone) and expunges
//   - after a disconnect or resume from sleep, what changed in between
//
// The password is read from the Secret Service keyring. Store it with:
//
//	secret-tool store --label='foonmail: USER on HOST' service foonmail/HOST username USER
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/zalando/go-keyring"
)

func main() {
	addr := flag.String("addr", "mail.uplink.fi:993", "IMAP server, host:port (implicit TLS)")
	user := flag.String("user", "", "login name (required)")
	mailbox := flag.String("mailbox", "INBOX", "mailbox to watch")
	check := flag.Duration("check", 5*time.Minute, "how often to verify the connection is alive")
	debug := flag.Bool("debug", false, "print the IMAP protocol to stderr, with credentials redacted")
	flag.Parse()

	out := log.New(os.Stdout, "", log.Ltime)
	if *user == "" {
		fmt.Fprintln(os.Stderr, "imap-probe: -user is required")
		flag.Usage()
		os.Exit(2)
	}
	host, _, err := net.SplitHostPort(*addr)
	if err != nil {
		out.Fatalf("bad -addr %q: %v", *addr, err)
	}

	service := "foonmail/" + host
	pass, err := keyring.Get(service, *user)
	if err != nil {
		if errors.Is(err, keyring.ErrNotFound) {
			fmt.Fprintf(os.Stderr, "No password in the keyring for %s on %s. Store it with:\n\n"+
				"  secret-tool store --label='foonmail: %s on %s' service %s username %s\n\n",
				*user, host, *user, host, service, *user)
			os.Exit(1)
		}
		out.Fatalf("reading the keyring: %v", err)
	}

	p := &prober{
		addr: *addr, user: *user, pass: pass, mailbox: *mailbox,
		check: *check, out: out,
	}
	if *debug {
		p.debug = newRedactWriter(os.Stderr)
	} else {
		p.debug = io.Discard
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	wake := watchResume(ctx, out)

	backoff := time.Second
	for {
		started := time.Now()
		err := p.session(ctx, wake)
		if ctx.Err() != nil {
			out.Printf("stopped")
			return
		}
		if time.Since(started) > time.Minute {
			backoff = time.Second // the connection was healthy for a while
		}
		if errors.Is(err, errResumed) {
			out.Printf("resumed from sleep: reconnecting now")
			backoff = time.Second
			continue
		}
		out.Printf("disconnected: %v; reconnecting in %s", err, backoff)
		select {
		case <-time.After(backoff):
		case <-wake: // resuming from sleep is a good moment to retry
		case <-ctx.Done():
			out.Printf("stopped")
			return
		}
		backoff = min(backoff*2, time.Minute)
	}
}

// watchResume detects resume from suspend. Go's monotonic clock uses
// CLOCK_MONOTONIC, which stops during suspend on Linux, while the wall
// clock keeps going; a gap between the two means the machine slept. A
// connection that was idle across a suspend is usually dead without
// either side knowing, so the session reconnects instead of waiting for
// TCP to notice.
func watchResume(ctx context.Context, out *log.Logger) <-chan struct{} {
	wake := make(chan struct{}, 1)
	go func() {
		const tick = 5 * time.Second
		t := time.NewTicker(tick)
		defer t.Stop()
		last := time.Now()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-t.C:
				slept := now.Round(0).Sub(last.Round(0)) - now.Sub(last)
				last = now
				if slept > 10*time.Second {
					out.Printf("clock jumped %s: the system was probably asleep", slept.Round(time.Second))
					select {
					case wake <- struct{}{}:
					default:
					}
				}
			}
		}
	}()
	return wake
}
