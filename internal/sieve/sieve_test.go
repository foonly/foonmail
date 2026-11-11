package sieve

import (
	"bufio"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func TestScriptRoundTrip(t *testing.T) {
	rules := []Rule{
		{Field: FromAddress, Value: "news@example.org", Folder: "Lists", MarkRead: true},
		{Field: FromDomain, Value: "shop.example", Folder: "Receipts"},
		{Field: List, Value: "dev.lists.example.org", Folder: "Lists.Dev"},
		{Field: ToAddress, Value: "kim+orders@example.org", Folder: "Orders"},
		{Field: Subject, Value: `say "hi" \ there`, Folder: "Misc"},
	}
	s := Script(rules)
	for _, want := range []string{
		`require ["fileinto", "mailbox", "imap4flags"];`,
		`if address :is "from" "news@example.org" {`,
		`    addflag "\\Seen";`,
		`    fileinto :create "Lists";`,
		`if address :domain :is "from" "shop.example" {`,
		`if header :contains "list-id" "dev.lists.example.org" {`,
		`if address :is ["to", "cc"] "kim+orders@example.org" {`,
		`if header :contains "subject" "say \"hi\" \\ there" {`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("script lacks %q:\n%s", want, s)
		}
	}
	got, byHand := Parse(s)
	if byHand || !slices.Equal(got, rules) {
		t.Errorf("Parse = %v, byHand %v", got, byHand)
	}
	if _, byHand := Parse(s + "\nkeep;\n"); !byHand {
		t.Error("an edited script wasn't noticed")
	}
	if got, byHand := Parse(Script(nil)); len(got) != 0 || byHand {
		t.Errorf("empty: %v %v", got, byHand)
	}
	if !strings.Contains(Script(rules[1:2]), `require ["fileinto", "mailbox"];`) {
		t.Error("imap4flags required without a rule that marks read")
	}
}

func TestRuleValid(t *testing.T) {
	ok := Rule{Field: FromAddress, Value: "a@b", Folder: "X"}
	if v := ok.Valid(); v != "" {
		t.Error(v)
	}
	for _, r := range []Rule{
		{Field: FromAddress, Folder: "X"},
		{Field: FromAddress, Value: "a@b"},
		{Field: "body", Value: "a", Folder: "X"},
		{Field: Subject, Value: "a\nb", Folder: "X"},
	} {
		if r.Valid() == "" {
			t.Errorf("%+v passed", r)
		}
	}
}

// fakeServer is a ManageSieve server for one connection: STARTTLS, PLAIN,
// and scripts kept in memory. A script containing "broken" is rejected.
func fakeServer(t *testing.T, ln net.Listener, cfg *tls.Config, scripts map[string]string, active *string) {
	conn, err := ln.Accept()
	if err != nil {
		return
	}
	defer conn.Close()
	caps := func(w *bufio.Writer, tlsOn bool) {
		w.WriteString("\"IMPLEMENTATION\" \"fake\"\r\n\"SIEVE\" \"fileinto mailbox imap4flags\"\r\n")
		if !tlsOn {
			w.WriteString("\"STARTTLS\"\r\n")
		}
		w.WriteString("\"SASL\" \"PLAIN LOGIN\"\r\n\"VERSION\" \"1.0\"\r\nOK \"ready\"\r\n")
		w.Flush()
	}
	r, w := bufio.NewReader(conn), bufio.NewWriter(conn)
	caps(w, false)
	// literal reads a {n+} literal that ends the line.
	literal := func(line string) string {
		i := strings.LastIndex(line, "{")
		var n int
		for _, c := range strings.TrimSuffix(line[i+1:], "+}") {
			n = n*10 + int(c-'0')
		}
		buf := make([]byte, n)
		if _, err := ioReadFull(r, buf); err != nil {
			t.Error(err)
		}
		r.ReadString('\n')
		return string(buf)
	}
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		cmd, arg, _ := strings.Cut(line, " ")
		switch cmd {
		case "STARTTLS":
			w.WriteString("OK \"go\"\r\n")
			w.Flush()
			tc := tls.Server(conn, cfg)
			if err := tc.Handshake(); err != nil {
				t.Error(err)
				return
			}
			conn = tc
			r, w = bufio.NewReader(conn), bufio.NewWriter(conn)
			caps(w, true)
			continue
		case "AUTHENTICATE":
			if !strings.Contains(arg, "AGtpbQBzZWNyZXQ=") { // \0kim\0secret
				w.WriteString("NO \"bad login\"\r\n")
			} else {
				w.WriteString("OK \"in\"\r\n")
			}
		case "LISTSCRIPTS":
			for name := range scripts {
				w.WriteString("\"" + name + "\"")
				if name == *active {
					w.WriteString(" ACTIVE")
				}
				w.WriteString("\r\n")
			}
			w.WriteString("OK\r\n")
		case "GETSCRIPT":
			s := scripts[strings.Trim(arg, `"`)]
			w.WriteString("{" + itoa(len(s)) + "}\r\n" + s + "\r\nOK\r\n")
		case "CHECKSCRIPT", "PUTSCRIPT":
			s := literal(line)
			if strings.Contains(s, "broken") {
				w.WriteString("NO \"line 3: unknown command 'broken'\"\r\n")
				break
			}
			if cmd == "PUTSCRIPT" {
				name, _, _ := strings.Cut(arg, " ")
				scripts[strings.Trim(name, `"`)] = s
			}
			w.WriteString("OK\r\n")
		case "SETACTIVE":
			*active = strings.Trim(arg, `"`)
			w.WriteString("OK\r\n")
		case "LOGOUT":
			w.WriteString("OK \"bye\"\r\n")
			w.Flush()
			return
		default:
			w.WriteString("NO \"unknown\"\r\n")
		}
		w.Flush()
	}
}

func TestClient(t *testing.T) {
	srv := httptest.NewUnstartedServer(nil) // just for its test certificate
	srv.StartTLS()
	cert := srv.TLS.Certificates[0]
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	srv.Close()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	scripts := map[string]string{"old": "keep;\n"}
	active := "old"
	go fakeServer(t, ln, &tls.Config{Certificates: []tls.Certificate{cert}}, scripts, &active)

	c, err := Dial(ln.Addr().String(), "kim", "secret", &tls.Config{RootCAs: pool, ServerName: "example.com"})
	if err != nil {
		t.Fatal(err)
	}
	list, err := c.ListScripts()
	if err != nil || len(list) != 1 || list[0] != (ScriptInfo{"old", true}) {
		t.Fatalf("ListScripts = %v, %v", list, err)
	}
	if s, err := c.GetScript("old"); err != nil || s != "keep;\n" {
		t.Errorf("GetScript = %q, %v", s, err)
	}
	if err := c.CheckScript("broken;\n"); !errors.Is(err, ErrRejected) || !strings.Contains(err.Error(), "unknown command") {
		t.Errorf("CheckScript of a broken script: %v", err)
	}
	script := Script([]Rule{{Field: FromAddress, Value: "a@b", Folder: "X"}})
	if err := c.PutScript(ScriptName, script); err != nil {
		t.Fatal(err)
	}
	if err := c.SetActive(ScriptName); err != nil {
		t.Fatal(err)
	}
	if s, err := c.GetScript(ScriptName); err != nil || s != script {
		t.Errorf("stored script = %q, %v", s, err)
	}
	if active != ScriptName {
		t.Errorf("active = %q", active)
	}
	c.Close()
}

func ioReadFull(r *bufio.Reader, buf []byte) (int, error) { return io.ReadFull(r, buf) }

func itoa(n int) string { return strconv.Itoa(n) }
