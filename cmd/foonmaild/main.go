// Command foonmaild is the foonmail daemon. It keeps the mail cache in step
// with every configured account and reports new mail.
//
// The tray icon and desktop notifications are not wired up yet; for now
// events are logged.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"

	"github.com/godbus/dbus/v5"
	"github.com/zalando/go-keyring"

	"foonmail/internal/cache"
	"foonmail/internal/config"
	"foonmail/internal/imapsync"
	"foonmail/internal/outbox"
	"foonmail/internal/rpc"
	"foonmail/internal/tray"
)

func main() {
	defaultPath, _ := config.Path()
	configPath := flag.String("config", defaultPath, "configuration file")
	verbose := flag.Bool("v", false, "log debug messages")
	flag.Parse()

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	if err := run(*configPath, log); err != nil {
		fmt.Fprintln(os.Stderr, "foonmaild:", err)
		if errors.Is(err, errLocked) {
			os.Exit(3) // the systemd unit doesn't restart on this
		}
		os.Exit(1)
	}
}

func run(configPath string, log *slog.Logger) error {
	cfg, err := config.Load(configPath)
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("no configuration at %s; copy config.example.toml there and edit it", configPath)
	}
	if err != nil {
		return err
	}

	lock, err := acquireLock(cfg.CacheDir)
	if err != nil {
		return err
	}
	defer lock.Close()

	db, err := cache.Open(filepath.Join(cfg.CacheDir, "mail.db"))
	if err != nil {
		return err
	}
	defer db.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Warn about missing passwords up front, but keep going: the keyring may
	// still be locked at login, and the sync loops retry.
	for i := range cfg.Accounts {
		if _, err := password(cfg.Accounts[i].IMAP); err != nil {
			log.Warn("password not available yet", "account", cfg.Accounts[i].Name, "err", err)
		}
	}

	waker := &imapsync.Waker{}
	go waker.Watch(ctx, log)

	h, err := newHub(ctx, cfg, db, nil, nil, log)
	if err != nil {
		return err
	}
	var syncers []*imapsync.Syncer
	copies := copier{}
	backends := map[int64]backend{}
	for i := range cfg.Accounts {
		acct := &cfg.Accounts[i]
		s, err := imapsync.New(ctx, imapsync.Options{
			Account:  acct,
			DB:       db,
			Log:      log,
			Password: password,
			Notify:   h.notify,
		})
		if err != nil {
			return fmt.Errorf("account %s: %w", acct.Name, err)
		}
		syncers = append(syncers, s)
		backends[s.AccountID()] = s
		copies[acct.Name] = s
	}

	// The outbox lives in the data directory: unsent mail can't be rebuilt
	// from the server. Without it the daemon still syncs.
	sender := &outbox.SMTP{Password: password}
	ob, err := outbox.Open(outbox.Options{
		DataDir:  cfg.DataDir,
		Accounts: cfg.Accounts,
		Sender:   sender,
		Copier:   copies,
		Notify:   h.notifyOutbox,
		Log:      log,
	})
	if err != nil {
		log.Error("the outbox can't be opened: sending is off", "err", err)
	} else {
		defer ob.Close()
		sender.OnSizeLimit = ob.RememberSizeLimit
		h.outbox = ob
	}

	// The session bus carries the tray, notifications and the GUI's
	// interface. Without it the daemon still syncs.
	if conn, err := dbus.ConnectSessionBus(); err != nil {
		log.Warn("no session bus: running without tray, notifications and GUI interface", "err", err)
	} else {
		defer conn.Close()
		closeDesktop := connectDesktop(h, conn, log)
		defer closeDesktop()
		var api outboxAPI // a nil *Outbox would be a non-nil interface
		if ob != nil {
			api = ob
		}
		svc, err := exportService(conn, db, backends, api, h, log)
		if err != nil {
			return err
		}
		h.signals = svc
	}
	go h.run(ctx)

	var wg sync.WaitGroup
	if ob != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ob.Run(ctx)
		}()
	}
	for _, s := range syncers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.Run(ctx, waker.Subscribe(), waker.Subscribe())
		}()
	}
	log.Info("foonmaild started", "accounts", len(cfg.Accounts), "cache", cfg.CacheDir)
	<-ctx.Done()
	log.Info("shutting down")
	wg.Wait()
	return nil
}

// connectDesktop attaches the tray icon and notifications to the hub. The
// daemon still syncs without them, for example on a desktop with no tray.
// It must run before h.run starts.
func connectDesktop(h *hub, conn *dbus.Conn, log *slog.Logger) (closeDesktop func()) {
	h.notifier = &lazyNotifier{conn: conn, log: log}
	h.gui = rpc.NewGUIClient(conn)
	t, err := tray.New(conn, "foonmail", tray.State{Status: tray.Active, IconName: "mail-read", Tooltip: "foonmail"},
		h.activate, log)
	if err != nil {
		log.Warn("no tray icon", "err", err)
	} else {
		h.tray = t
	}
	return func() {
		if t != nil {
			t.Close()
		}
	}
}

// password reads a server's password from the Secret Service keyring.
func password(s config.Server) (string, error) {
	service, user := s.SecretService()
	p, err := keyring.Get(service, user)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", fmt.Errorf("no password in the keyring for %s on %s; store it with: "+
			"secret-tool store --label='foonmail: %s on %s' service %s username %s",
			user, s.Host, user, s.Host, service, user)
	}
	return p, err
}
