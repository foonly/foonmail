// Package config loads and validates foonmail's TOML configuration file.
//
// The file lives at $XDG_CONFIG_HOME/foonmail/config.toml unless
// FOONMAIL_CONFIG points elsewhere. See config.example.toml in the
// repository root for an annotated example. Passwords are never stored in
// the file; they come from the Secret Service keyring (see Server.SecretService).
package config

import (
	"errors"
	"fmt"
	"net/mail"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// Config is the whole configuration file.
type Config struct {
	// CacheDir holds the mail cache database. Default: $XDG_CACHE_HOME/foonmail.
	CacheDir string    `toml:"cache_dir"`
	Accounts []Account `toml:"account"`
}

// Account is one mail account.
type Account struct {
	// Name is a short, unique identifier used in the cache and in logs.
	Name        string   `toml:"name"`
	Email       string   `toml:"email"`
	DisplayName string   `toml:"display_name"`
	Aliases     []string `toml:"aliases"`

	// AppendSent copies sent mail to the Sent folder. Gmail does this
	// itself, so it must be false there. Default: true.
	AppendSent *bool `toml:"append_sent"`
	// NotifyDetails shows sender and subject in new-mail notifications.
	// Default: true.
	NotifyDetails *bool `toml:"notify_details"`

	IMAP    Server  `toml:"imap"`
	SMTP    Server  `toml:"smtp"`
	Folders Folders `toml:"folders"`
}

// Security is how a connection is encrypted.
type Security string

const (
	SecurityTLS      Security = "tls"      // implicit TLS (993, 465)
	SecurityStartTLS Security = "starttls" // upgrade a plain connection (143, 587)
)

// Server is an IMAP or SMTP server.
type Server struct {
	Host     string   `toml:"host"`
	Port     int      `toml:"port"`
	Security Security `toml:"security"`
	Username string   `toml:"username"`
}

// Address is host:port.
func (s Server) Address() string { return fmt.Sprintf("%s:%d", s.Host, s.Port) }

// SecretService returns the attributes the password is stored under in
// the keyring: service "foonmail/<host>" and username "<username>".
func (s Server) SecretService() (service, username string) {
	return "foonmail/" + s.Host, s.Username
}

// Folders says which folders are synced in the background. INBOX is always
// watched live with IDLE and is the only folder that alerts; other folders
// are synced when the GUI opens them.
type Folders struct {
	// Watched folders are polled with STATUS and synced when they change.
	// A name starting with "@" refers to a special-use folder: "@sent",
	// "@drafts", "@trash", "@junk", "@archive". Default: ["@sent"], so a
	// conversation can show your own replies.
	Watched []string `toml:"watched"`
	// PollInterval is how often watched folders are checked. Default: 5m.
	PollInterval time.Duration `toml:"poll_interval"`
}

// SpecialUse maps "@name" folder references to IMAP special-use attributes.
var SpecialUse = map[string]string{
	"@sent":    `\Sent`,
	"@drafts":  `\Drafts`,
	"@trash":   `\Trash`,
	"@junk":    `\Junk`,
	"@archive": `\Archive`,
}

const (
	defaultPollInterval = 5 * time.Minute
	minPollInterval     = 30 * time.Second
)

var accountName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// Path returns the configuration file path.
func Path() (string, error) {
	if p := os.Getenv("FOONMAIL_CONFIG"); p != "" {
		return p, nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "foonmail", "config.toml"), nil
}

// LoadDefault loads the file at Path.
func LoadDefault() (*Config, error) {
	p, err := Path()
	if err != nil {
		return nil, err
	}
	return Load(p)
}

// Load reads, defaults and validates a configuration file. Unknown keys
// are errors, so a typo doesn't silently fall back to a default.
func Load(path string) (*Config, error) {
	var c Config
	md, err := toml.DecodeFile(path, &c)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	var problems []string
	for _, k := range md.Undecoded() {
		problems = append(problems, fmt.Sprintf("unknown setting %q", k.String()))
	}
	if err := c.applyDefaults(); err != nil {
		return nil, err
	}
	problems = append(problems, c.validate()...)
	if len(problems) > 0 {
		return nil, &Error{Path: path, Problems: problems}
	}
	return &c, nil
}

// Error lists everything wrong with a configuration file at once.
type Error struct {
	Path     string
	Problems []string
}

func (e *Error) Error() string {
	return fmt.Sprintf("%s:\n  %s", e.Path, strings.Join(e.Problems, "\n  "))
}

func (c *Config) applyDefaults() error {
	if c.CacheDir == "" {
		dir, err := os.UserCacheDir()
		if err != nil {
			return err
		}
		c.CacheDir = filepath.Join(dir, "foonmail")
	} else if strings.HasPrefix(c.CacheDir, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		c.CacheDir = filepath.Join(home, c.CacheDir[2:])
	}
	for i := range c.Accounts {
		a := &c.Accounts[i]
		if a.AppendSent == nil {
			a.AppendSent = ptr(true)
		}
		if a.NotifyDetails == nil {
			a.NotifyDetails = ptr(true)
		}
		defaultServer(&a.IMAP, 993, 143)
		if a.SMTP.Host == "" {
			a.SMTP.Host = a.IMAP.Host
		}
		if a.SMTP.Username == "" {
			a.SMTP.Username = a.IMAP.Username
		}
		defaultServer(&a.SMTP, 465, 587)
		if a.Folders.Watched == nil {
			a.Folders.Watched = []string{"@sent"}
		}
		if a.Folders.PollInterval == 0 {
			a.Folders.PollInterval = defaultPollInterval
		}
	}
	return nil
}

func defaultServer(s *Server, tlsPort, startTLSPort int) {
	if s.Security == "" {
		s.Security = SecurityTLS
	}
	if s.Port == 0 {
		if s.Security == SecurityStartTLS {
			s.Port = startTLSPort
		} else {
			s.Port = tlsPort
		}
	}
}

func (c *Config) validate() []string {
	var p []string
	add := func(format string, args ...any) { p = append(p, fmt.Sprintf(format, args...)) }

	if !filepath.IsAbs(c.CacheDir) {
		add("cache_dir must be an absolute path or start with ~/: %q", c.CacheDir)
	}
	if len(c.Accounts) == 0 {
		add("no accounts: add an [[account]] section")
	}
	seen := map[string]bool{}
	for i, a := range c.Accounts {
		where := fmt.Sprintf("account %d", i+1)
		if a.Name != "" {
			where = fmt.Sprintf("account %q", a.Name)
		}
		switch {
		case a.Name == "":
			add("%s: name is required", where)
		case !accountName.MatchString(a.Name):
			add("%s: name may only contain a-z, 0-9, _ and -, starting with a letter or digit", where)
		case seen[a.Name]:
			add("%s: name is used by more than one account", where)
		}
		seen[a.Name] = true

		if a.Email == "" {
			add("%s: email is required", where)
		} else if _, err := mail.ParseAddress(a.Email); err != nil {
			add("%s: email %q is not a valid address", where, a.Email)
		}
		for _, al := range a.Aliases {
			if _, err := mail.ParseAddress(al); err != nil {
				add("%s: alias %q is not a valid address", where, al)
			}
		}
		p = append(p, validateServer(where+": imap", a.IMAP)...)
		p = append(p, validateServer(where+": smtp", a.SMTP)...)

		dup := map[string]bool{}
		for _, f := range a.Folders.Watched {
			switch {
			case f == "":
				add("%s: folders.watched has an empty name", where)
			case strings.HasPrefix(f, "@") && SpecialUse[f] == "":
				add("%s: folders.watched: unknown special folder %q (use %s)", where, f, specialNames())
			case strings.EqualFold(f, "INBOX"):
				add("%s: folders.watched: INBOX is always watched live and needn't be listed", where)
			case dup[f]:
				add("%s: folders.watched lists %q twice", where, f)
			}
			dup[f] = true
		}
		if a.Folders.PollInterval < minPollInterval {
			add("%s: folders.poll_interval must be at least %s", where, minPollInterval)
		}
	}
	return p
}

func validateServer(where string, s Server) []string {
	var p []string
	if s.Host == "" {
		p = append(p, where+".host is required")
	}
	if s.Username == "" {
		p = append(p, where+".username is required")
	}
	if s.Security != SecurityTLS && s.Security != SecurityStartTLS {
		p = append(p, fmt.Sprintf("%s.security must be %q or %q, not %q", where, SecurityTLS, SecurityStartTLS, s.Security))
	}
	if s.Port < 1 || s.Port > 65535 {
		p = append(p, fmt.Sprintf("%s.port %d is out of range", where, s.Port))
	}
	return p
}

func specialNames() string {
	names := make([]string, 0, len(SpecialUse))
	for k := range SpecialUse {
		names = append(names, k)
	}
	slices.Sort(names)
	return strings.Join(names, ", ")
}

// Account returns the account with the given name.
func (c *Config) Account(name string) (*Account, error) {
	for i := range c.Accounts {
		if c.Accounts[i].Name == name {
			return &c.Accounts[i], nil
		}
	}
	return nil, errors.New("no account named " + name)
}

// Addresses returns the account's own addresses: email plus aliases,
// lowercased, for recognizing mail from yourself.
func (a *Account) Addresses() []string {
	var out []string
	for _, s := range append([]string{a.Email}, a.Aliases...) {
		if addr, err := mail.ParseAddress(s); err == nil {
			out = append(out, strings.ToLower(addr.Address))
		}
	}
	return out
}

func ptr[T any](v T) *T { return &v }
