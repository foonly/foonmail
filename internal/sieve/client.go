// Package sieve manages server-side mail filters: a ManageSieve client
// (RFC 5804) and the rules foonmail keeps as a Sieve script (RFC 5228).
package sieve

import (
	"bufio"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"
)

// ErrRejected is a NO from the server, such as a script that doesn't
// compile; the error's text is the server's reason.
var ErrRejected = errors.New("rejected by the server")

// Client is a ManageSieve connection, logged in.
type Client struct {
	conn net.Conn
	r    *bufio.Reader
	caps map[string]string
}

// Dial connects to addr (host:port), upgrades to TLS with STARTTLS and
// logs in with PLAIN. tlsConfig may be nil; the server name is set from
// addr if it is empty.
func Dial(addr, user, pass string, tlsConfig *tls.Config) (*Client, error) {
	conn, err := net.DialTimeout("tcp", addr, 15*time.Second)
	if err != nil {
		return nil, err
	}
	c, err := start(conn, addr, user, pass, tlsConfig)
	if err != nil {
		conn.Close()
		return nil, err
	}
	return c, nil
}

func start(conn net.Conn, addr, user, pass string, tlsConfig *tls.Config) (*Client, error) {
	c := &Client{conn: conn, r: bufio.NewReader(conn)}
	c.deadline()
	if err := c.readCaps(); err != nil {
		return nil, fmt.Errorf("greeting: %w", err)
	}
	if _, ok := c.caps["STARTTLS"]; !ok {
		return nil, errors.New("the server doesn't offer STARTTLS; not sending the password in clear")
	}
	if _, err := c.cmd("STARTTLS"); err != nil {
		return nil, fmt.Errorf("STARTTLS: %w", err)
	}
	cfg := &tls.Config{}
	if tlsConfig != nil {
		cfg = tlsConfig.Clone()
	}
	if cfg.ServerName == "" {
		cfg.ServerName, _, _ = net.SplitHostPort(addr)
	}
	tc := tls.Client(conn, cfg)
	if err := tc.Handshake(); err != nil {
		return nil, fmt.Errorf("TLS: %w", err)
	}
	c.conn, c.r = tc, bufio.NewReader(tc)
	c.deadline()
	// After STARTTLS the server sends its capabilities again (RFC 5804 2.2).
	if err := c.readCaps(); err != nil {
		return nil, fmt.Errorf("capabilities after TLS: %w", err)
	}
	if !strings.Contains(" "+c.caps["SASL"]+" ", " PLAIN ") {
		return nil, fmt.Errorf("the server doesn't offer PLAIN login (SASL %q)", c.caps["SASL"])
	}
	cred := base64.StdEncoding.EncodeToString([]byte("\x00" + user + "\x00" + pass))
	if _, err := c.cmd("AUTHENTICATE " + quote("PLAIN") + " " + quote(cred)); err != nil {
		return nil, fmt.Errorf("login: %w", err)
	}
	return c, nil
}

// Close logs out and closes the connection.
func (c *Client) Close() error {
	c.deadline()
	_, _ = c.cmd("LOGOUT")
	return c.conn.Close()
}

// ScriptInfo is a script on the server.
type ScriptInfo struct {
	Name   string
	Active bool
}

// ListScripts lists the scripts on the server.
func (c *Client) ListScripts() ([]ScriptInfo, error) {
	lines, err := c.cmd("LISTSCRIPTS")
	if err != nil {
		return nil, err
	}
	var out []ScriptInfo
	for _, l := range lines {
		if len(l) == 0 {
			continue
		}
		s := ScriptInfo{Name: l[0]}
		s.Active = len(l) > 1 && strings.EqualFold(l[1], "ACTIVE")
		out = append(out, s)
	}
	return out, nil
}

// GetScript returns a script's text.
func (c *Client) GetScript(name string) (string, error) {
	lines, err := c.cmd("GETSCRIPT " + quote(name))
	if err != nil {
		return "", err
	}
	if len(lines) == 0 || len(lines[0]) == 0 {
		return "", errors.New("GETSCRIPT: no script in the answer")
	}
	return lines[0][0], nil
}

// CheckScript asks the server whether a script is valid without storing it.
func (c *Client) CheckScript(text string) error {
	_, err := c.cmd("CHECKSCRIPT " + literal(text))
	return err
}

// PutScript stores a script; the server checks it first.
func (c *Client) PutScript(name, text string) error {
	_, err := c.cmd("PUTSCRIPT " + quote(name) + " " + literal(text))
	return err
}

// SetActive makes a script the active one; "" deactivates all.
func (c *Client) SetActive(name string) error {
	_, err := c.cmd("SETACTIVE " + quote(name))
	return err
}

func (c *Client) deadline() { _ = c.conn.SetDeadline(time.Now().Add(time.Minute)) }

// cmd sends a command and reads the response lines up to OK, NO or BYE.
// Each line is a list of its strings and atoms.
func (c *Client) cmd(line string) ([][]string, error) {
	c.deadline()
	if _, err := io.WriteString(c.conn, line+"\r\n"); err != nil {
		return nil, err
	}
	return c.response()
}

func (c *Client) readCaps() error {
	lines, err := c.response()
	if err != nil {
		return err
	}
	c.caps = map[string]string{}
	for _, l := range lines {
		if len(l) == 0 {
			continue
		}
		v := ""
		if len(l) > 1 {
			v = l[1]
		}
		c.caps[strings.ToUpper(l[0])] = v
	}
	return nil
}

// response reads lines until the final OK, NO or BYE.
func (c *Client) response() ([][]string, error) {
	var lines [][]string
	for {
		items, err := c.readLine()
		if err != nil {
			return nil, err
		}
		if len(items) > 0 {
			switch strings.ToUpper(items[0]) {
			case "OK":
				return lines, nil
			case "NO", "BYE":
				msg := strings.ToUpper(items[0])
				for _, it := range items[1:] {
					if !strings.HasPrefix(it, "(") { // skip a response code
						msg = it
					}
				}
				return nil, fmt.Errorf("%w: %s", ErrRejected, strings.TrimSpace(msg))
			}
		}
		lines = append(lines, items)
	}
}

// readLine reads one response line: atoms, quoted strings, literals
// ({n} then n bytes) and a parenthesised response code kept as one item.
func (c *Client) readLine() ([]string, error) {
	var items []string
	for {
		b, err := c.r.ReadByte()
		if err != nil {
			return nil, err
		}
		switch {
		case b == '\r':
		case b == '\n':
			return items, nil
		case b == ' ':
		case b == '"':
			s, err := c.readQuoted()
			if err != nil {
				return nil, err
			}
			items = append(items, s)
		case b == '{':
			s, err := c.readLiteral()
			if err != nil {
				return nil, err
			}
			items = append(items, s)
		case b == '(':
			s, err := c.r.ReadString(')')
			if err != nil {
				return nil, err
			}
			items = append(items, "("+s)
		default:
			var sb strings.Builder
			sb.WriteByte(b)
			for {
				nb, err := c.r.ReadByte()
				if err != nil {
					return nil, err
				}
				if nb == ' ' || nb == '\r' || nb == '\n' {
					_ = c.r.UnreadByte()
					break
				}
				sb.WriteByte(nb)
			}
			items = append(items, sb.String())
		}
	}
}

func (c *Client) readQuoted() (string, error) {
	var sb strings.Builder
	for {
		b, err := c.r.ReadByte()
		if err != nil {
			return "", err
		}
		switch b {
		case '"':
			return sb.String(), nil
		case '\\':
			nb, err := c.r.ReadByte()
			if err != nil {
				return "", err
			}
			sb.WriteByte(nb)
		default:
			sb.WriteByte(b)
		}
	}
}

func (c *Client) readLiteral() (string, error) {
	spec, err := c.r.ReadString('}')
	if err != nil {
		return "", err
	}
	n, err := strconv.Atoi(strings.TrimSuffix(strings.TrimSuffix(spec, "}"), "+"))
	if err != nil || n < 0 || n > 1<<24 {
		return "", fmt.Errorf("bad literal {%s", spec)
	}
	if crlf, err := c.r.ReadString('\n'); err != nil || strings.TrimSpace(crlf) != "" {
		return "", fmt.Errorf("bad literal start %q", crlf)
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(c.r, buf); err != nil {
		return "", err
	}
	return string(buf), nil
}

func quote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// literal sends s as a non-synchronizing literal, which every ManageSieve
// server accepts (RFC 5804 1.6).
func literal(s string) string {
	return "{" + strconv.Itoa(len(s)) + "+}\r\n" + s
}
