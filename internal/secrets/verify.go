package secrets

import (
	"context"
	"crypto/tls"
	"fmt"
	"time"

	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-sasl"
	"github.com/emersion/go-smtp"

	"foonmail/internal/config"
)

// Verify logs in to a server with a password, to catch a mistyped one
// before it is stored. role is "IMAP" or "SMTP".
func Verify(ctx context.Context, role string, s config.Server, password string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	// The login runs aside, so a server that never answers is given up on;
	// the command exits soon after anyway.
	done := make(chan error, 1)
	go func() {
		if role == "SMTP" {
			done <- verifySMTP(s, password)
		} else {
			done <- verifyIMAP(s, password)
		}
	}()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return fmt.Errorf("%s didn't answer in time", s.Address())
	}
}

func verifyIMAP(s config.Server, password string) error {
	var c *imapclient.Client
	var err error
	if s.Security == config.SecurityStartTLS {
		c, err = imapclient.DialStartTLS(s.Address(), nil)
	} else {
		c, err = imapclient.DialTLS(s.Address(), nil)
	}
	if err != nil {
		return err
	}
	defer c.Close()
	if err := c.Login(s.Username, password).Wait(); err != nil {
		return err
	}
	return c.Logout().Wait()
}

func verifySMTP(s config.Server, password string) error {
	var c *smtp.Client
	var err error
	if s.Security == config.SecurityStartTLS {
		c, err = smtp.DialStartTLS(s.Address(), &tls.Config{ServerName: s.Host})
	} else {
		c, err = smtp.DialTLS(s.Address(), &tls.Config{ServerName: s.Host})
	}
	if err != nil {
		return err
	}
	defer c.Close()
	if err := c.Auth(sasl.NewPlainClient("", s.Username, password)); err != nil {
		return err
	}
	return c.Quit()
}
