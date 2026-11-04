package cache

import (
	"fmt"
	"path/filepath"
	"testing"
)

// BenchmarkAddMessages measures storing one sync chunk (500 messages).
func BenchmarkAddMessages(b *testing.B) {
	db, err := Open(filepath.Join(b.TempDir(), "mail.db"))
	if err != nil {
		b.Fatal(err)
	}
	defer db.Close()
	acct, _ := db.EnsureAccount(ctx, "a")
	f, _ := db.EnsureFolder(ctx, acct, "INBOX")
	uid := uint32(0)
	b.ResetTimer()
	for b.Loop() {
		msgs := make([]NewMessage, 500)
		for i := range msgs {
			uid++
			msgs[i] = msg(uid, fmt.Sprintf("%d@x", uid), "Subject line for benchmarking", "Bob Example")
		}
		if err := db.AddMessages(ctx, f.ID, msgs); err != nil {
			b.Fatal(err)
		}
	}
}
