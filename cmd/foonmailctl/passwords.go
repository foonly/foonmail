package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"golang.org/x/term"

	"foonmail/internal/config"
	"foonmail/internal/secrets"
)

const passwordsUsage = `usage: foonmailctl passwords [-check] [-change ACCOUNT] [-no-verify]

Lists the passwords the configured accounts need and whether the keyring
has them, then asks for the missing ones. Each is checked by logging in to
the server before it is stored.

  -check            also log in with the stored passwords, to see they still work
  -change ACCOUNT   enter ACCOUNT's password again, though one is stored
  -no-verify        store what is typed without logging in first
`

// maxAttempts is how often a password is asked for before moving on.
const maxAttempts = 3

// passwordTool is foonmailctl passwords, with its terminal and keyring
// replaceable for tests.
type passwordTool struct {
	cfg         *config.Config
	out         io.Writer
	read        func(prompt string) (string, error) // reads a password without echo
	interactive func() bool                         // whether passwords can be typed; nil: assume so
	verify      func(ctx context.Context, role string, s config.Server, password string) error
	store       func(s config.Server, password string) error
}

func runPasswords(args []string) error {
	fs := flag.NewFlagSet("passwords", flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, passwordsUsage) }
	check := fs.Bool("check", false, "")
	change := fs.String("change", "", "")
	noVerify := fs.Bool("no-verify", false, "")
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}
	if fs.NArg() > 0 {
		fs.Usage()
		os.Exit(2)
	}
	cfg, err := config.LoadDefault()
	if err != nil {
		return err
	}
	t := &passwordTool{cfg: cfg, out: os.Stdout, read: readHidden, verify: secrets.Verify, store: secrets.Store,
		interactive: func() bool { return term.IsTerminal(int(os.Stdin.Fd())) }}
	if *noVerify {
		t.verify = nil
	}
	return t.run(context.Background(), *check, *change)
}

// readHidden reads a line from the terminal without showing it.
func readHidden(prompt string) (string, error) {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return "", errors.New("not a terminal: run foonmailctl passwords in one to type passwords")
	}
	fmt.Fprint(os.Stderr, prompt)
	b, err := term.ReadPassword(fd)
	fmt.Fprintln(os.Stderr)
	return string(b), err
}

func (t *passwordTool) run(ctx context.Context, check bool, change string) error {
	if change != "" && !hasAccount(t.cfg, change) {
		return fmt.Errorf("no account named %q in the configuration", change)
	}
	needs := secrets.Needs(t.cfg)
	t.list(needs)

	if check {
		fmt.Fprintln(t.out)
		for _, n := range needs {
			if n.State != secrets.Stored && n.State != secrets.Shared {
				continue
			}
			p, err := secrets.Lookup(t.cfg, n.Server)
			if errors.Is(err, secrets.ErrMissing) {
				continue // shares a password that is missing, listed above
			}
			if err == nil && t.verify != nil {
				err = t.verify(ctx, n.Role, n.Server, p)
			}
			result := "logged in"
			if err != nil {
				result = "FAILED: " + err.Error()
			}
			fmt.Fprintf(t.out, "%s %s (%s on %s): %s\n", n.Account, n.Role, n.Server.Username, n.Server.Host, result)
		}
	}

	toAsk := 0
	for _, n := range needs {
		if n.State == secrets.Missing || (n.Account == change && n.State != secrets.Shared) {
			toAsk++
		}
	}
	if toAsk > 0 && t.interactive != nil && !t.interactive() {
		return fmt.Errorf("%d password(s) to enter: run foonmailctl passwords in a terminal", toAsk)
	}

	stored, missing := 0, 0
	for _, n := range needs {
		ask := n.State == secrets.Missing || (n.Account == change && n.State != secrets.Shared)
		if n.State == secrets.Unknown {
			fmt.Fprintf(t.out, "\n%s %s: can't read the keyring: %v\n", n.Account, n.Role, n.Err)
			missing++
			continue
		}
		if !ask {
			continue
		}
		// An SMTP server may have just got its password with its IMAP server's.
		if n.Role == "SMTP" && n.State == secrets.Missing {
			if _, err := secrets.Lookup(t.cfg, n.Server); err == nil {
				continue
			}
		}
		ok, err := t.ask(ctx, n)
		if err != nil {
			return err
		}
		if ok {
			stored++
		} else {
			missing++
		}
	}

	switch {
	case stored > 0:
		fmt.Fprintln(t.out, "\nfoonmaild picks up the new passwords when it next connects; to connect now:")
		fmt.Fprintln(t.out, "  systemctl --user restart foonmaild")
	case missing == 0 && change == "":
		fmt.Fprintln(t.out, "\nEvery password is in the keyring.")
	}
	if missing > 0 {
		return fmt.Errorf("%d password(s) still missing", missing)
	}
	return nil
}

// list prints which passwords are there.
func (t *passwordTool) list(needs []secrets.Need) {
	w := tabwriter.NewWriter(t.out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ACCOUNT\tSERVER\tLOGIN\tPASSWORD")
	for _, n := range needs {
		fmt.Fprintf(w, "%s\t%s %s\t%s\t%s\n", n.Account, n.Role, n.Server.Host, n.Server.Username, n.State)
	}
	w.Flush()
}

// ask reads a password for one server, checks it and stores it. An empty
// answer skips the server.
func (t *passwordTool) ask(ctx context.Context, n secrets.Need) (bool, error) {
	fmt.Fprintf(t.out, "\n%s: %s login %s on %s\n", n.Account, n.Role, n.Server.Username, n.Server.Host)
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		p, err := t.read("Password (empty to skip): ")
		if err != nil {
			return false, err
		}
		if p == "" {
			fmt.Fprintln(t.out, "Skipped.")
			return false, nil
		}
		if t.verify != nil {
			fmt.Fprintf(t.out, "Logging in to %s… ", n.Server.Host)
			if err := t.verify(ctx, n.Role, n.Server, p); err != nil {
				fmt.Fprintf(t.out, "failed: %v\n", err)
				if attempt < maxAttempts {
					fmt.Fprintln(t.out, "Try again, or press Enter to skip. (-no-verify stores without logging in.)")
				}
				continue
			}
			fmt.Fprintln(t.out, "ok.")
		}
		if err := t.store(n.Server, p); err != nil {
			return false, fmt.Errorf("storing the password: %w", err)
		}
		fmt.Fprintln(t.out, "Stored in the keyring.")
		return true, nil
	}
	fmt.Fprintln(t.out, "Giving up on this one for now.")
	return false, nil
}

func hasAccount(cfg *config.Config, name string) bool {
	for _, a := range cfg.Accounts {
		if strings.EqualFold(a.Name, name) {
			return true
		}
	}
	return false
}
