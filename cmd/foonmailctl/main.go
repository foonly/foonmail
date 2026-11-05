// Command foonmailctl talks to a running foonmaild over D-Bus.
//
//	foonmailctl status                    accounts: online, unread, new
//	foonmailctl send ACCOUNT FILE         queue a message (FILE "-" reads stdin)
//	foonmailctl outbox                    list unfinished and recent messages
//	foonmailctl undo ID                   take a message back during the undo window
//	foonmailctl resolve ID resend|sent    settle an uncertain or failed message
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"text/tabwriter"
	"time"

	"github.com/godbus/dbus/v5"

	"foonmail/internal/rpc"
)

const usage = `usage:
  foonmailctl status
  foonmailctl send ACCOUNT FILE      (FILE "-" reads the message from stdin)
  foonmailctl outbox
  foonmailctl undo ID
  foonmailctl resolve ID resend|sent
`

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "foonmailctl:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return err
	}
	defer conn.Close()
	c := rpc.NewClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	switch cmd := args[0]; {
	case cmd == "status" && len(args) == 1:
		st, err := c.Status(ctx)
		if err != nil {
			return err
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "ACCOUNT\tONLINE\tUNREAD\tNEW\tERROR")
		for _, a := range st {
			fmt.Fprintf(w, "%s\t%v\t%d\t%d\t%s\n", a.Name, a.Online, a.Unread, a.New, a.Error)
		}
		return w.Flush()

	case cmd == "send" && len(args) == 3:
		var raw []byte
		if args[2] == "-" {
			raw, err = io.ReadAll(os.Stdin)
		} else {
			raw, err = os.ReadFile(args[2])
		}
		if err != nil {
			return err
		}
		id, err := c.Send(ctx, args[1], raw)
		if err != nil {
			return err
		}
		fmt.Printf("queued as %d; it goes out in 10 seconds unless you run: foonmailctl undo %d\n", id, id)
		return nil

	case cmd == "outbox" && len(args) == 1:
		items, err := c.Outbox(ctx)
		if err != nil {
			return err
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tSTATE\tACCOUNT\tTO\tSUBJECT\tQUEUED\tERROR")
		for _, it := range items {
			fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\t%s\t%s\n", it.ID, it.State, it.Account, it.To, it.Subject,
				time.Unix(it.Created, 0).Format("2 Jan 15:04"), it.Error)
		}
		return w.Flush()

	case cmd == "undo" && len(args) == 2:
		id, err := strconv.ParseInt(args[1], 10, 64)
		if err != nil {
			return fmt.Errorf("bad id %q", args[1])
		}
		if err := c.Undo(ctx, id); err != nil {
			return err
		}
		fmt.Println("undone; it was moved to Drafts")
		return nil

	case cmd == "resolve" && len(args) == 3 && (args[2] == "resend" || args[2] == "sent"):
		id, err := strconv.ParseInt(args[1], 10, 64)
		if err != nil {
			return fmt.Errorf("bad id %q", args[1])
		}
		return c.Resolve(ctx, id, args[2] == "resend")
	}
	fmt.Fprint(os.Stderr, usage)
	os.Exit(2)
	return nil
}
