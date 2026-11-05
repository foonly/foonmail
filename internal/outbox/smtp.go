package outbox

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/emersion/go-sasl"
	"github.com/emersion/go-smtp"

	"foonmail/internal/config"
)

// Kinds of send failure, which decide what happens next.
var (
	// ErrTemporary: the server didn't take the message; try again later.
	ErrTemporary = errors.New("temporary failure")
	// ErrPermanent: the server refused the message; retrying won't help.
	ErrPermanent = errors.New("permanent failure")
	// ErrUncertain: the message was handed over but no answer came back,
	// so it may or may not have been accepted.
	ErrUncertain = errors.New("no answer after the message was handed over")
)

// SMTP submits messages to an account's SMTP server.
type SMTP struct {
	Password func(config.Server) (string, error)
	// Dial connects to the server. nil: TLS or STARTTLS as configured.
	// Tests return a plain connection.
	Dial func(ctx context.Context, s config.Server) (*smtp.Client, error)
	// OnSizeLimit is told the server's SIZE limit whenever it announces one.
	OnSizeLimit func(account string, bytes int)
	Timeout     time.Duration // whole transaction; default 2 minutes
}

// Send submits one message. The error wraps ErrTemporary, ErrPermanent or
// ErrUncertain.
func (s *SMTP) Send(ctx context.Context, acct *config.Account, from string, rcpts []string, data []byte) error {
	timeout := s.Timeout
	if timeout == 0 {
		timeout = 2 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	dial := s.Dial
	if dial == nil {
		dial = dialSMTP
	}
	c, err := dial(ctx, acct.SMTP)
	if err != nil {
		return fmt.Errorf("%w: connecting: %v", ErrTemporary, err)
	}
	defer c.Close()
	// Bound every read and write by the deadline.
	stop := context.AfterFunc(ctx, func() { c.Close() })
	defer stop()

	before := func(stage string, err error) error {
		return fmt.Errorf("%w: %s: %v", classify(err, ErrTemporary), stage, err)
	}
	if err := c.Hello("localhost"); err != nil {
		return before("EHLO", err)
	}
	if limit, ok := c.MaxMessageSize(); ok && limit > 0 {
		if s.OnSizeLimit != nil {
			s.OnSizeLimit(acct.Name, limit)
		}
		if len(data) > limit {
			return fmt.Errorf("%w: the message is %d bytes and the server accepts at most %d", ErrPermanent, len(data), limit)
		}
	}
	if ok, _ := c.Extension("AUTH"); ok {
		pass, err := s.Password(acct.SMTP)
		if err != nil {
			return fmt.Errorf("%w: password: %v", ErrTemporary, err)
		}
		if !c.SupportsAuth("PLAIN") {
			return fmt.Errorf("%w: the server offers no PLAIN login", ErrPermanent)
		}
		if err := c.Auth(sasl.NewPlainClient("", acct.SMTP.Username, pass)); err != nil {
			return before("login", err)
		}
	}
	if err := c.Mail(from, &smtp.MailOptions{Size: int64(len(data))}); err != nil {
		return before("MAIL FROM", err)
	}
	for _, r := range rcpts {
		if err := c.Rcpt(r, nil); err != nil {
			return before("RCPT TO "+r, err)
		}
	}
	w, err := c.Data()
	if err != nil {
		return before("DATA", err)
	}
	if _, err := w.Write(data); err != nil {
		// The server may have part of the message; without the final dot it
		// must discard it.
		return fmt.Errorf("%w: writing the message: %v", ErrTemporary, err)
	}
	// From here on the server has the whole message. An explicit reply is
	// definite; silence is not.
	if err := w.Close(); err != nil {
		var se *smtp.SMTPError
		if errors.As(err, &se) {
			return before("the server's answer", err)
		}
		return fmt.Errorf("%w: %v", ErrUncertain, err)
	}
	_ = c.Quit()
	return nil
}

// classify maps a protocol error to temporary or permanent by its reply
// code; anything without one (a network error) gets def.
func classify(err error, def error) error {
	var se *smtp.SMTPError
	if errors.As(err, &se) {
		if se.Code >= 500 {
			return ErrPermanent
		}
		return ErrTemporary
	}
	return def
}

func dialSMTP(ctx context.Context, s config.Server) (*smtp.Client, error) {
	d := &net.Dialer{Timeout: 30 * time.Second}
	tlsConf := &tls.Config{ServerName: s.Host}
	if s.Security == config.SecurityStartTLS {
		conn, err := d.DialContext(ctx, "tcp", s.Address())
		if err != nil {
			return nil, err
		}
		c, err := smtp.NewClientStartTLS(conn, tlsConf)
		if err != nil {
			conn.Close()
		}
		return c, err
	}
	conn, err := (&tls.Dialer{NetDialer: d, Config: tlsConf}).DialContext(ctx, "tcp", s.Address())
	if err != nil {
		return nil, err
	}
	return smtp.NewClient(conn), nil
}
