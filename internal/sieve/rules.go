package sieve

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ScriptName is the script foonmail writes and activates.
const ScriptName = "foonmail"

// Field is what a rule matches.
type Field string

const (
	FromAddress Field = "from"        // the sender's address, exactly
	FromDomain  Field = "from-domain" // the sender's domain
	List        Field = "list"        // a mailing list: List-Id contains the value
	ToAddress   Field = "to"          // a recipient in To or Cc, as for an alias
	Subject     Field = "subject"     // the subject contains the value
)

// Fields in the order the filter form offers them.
var Fields = []Field{FromAddress, FromDomain, List, ToAddress, Subject}

// Label names a field for people.
func (f Field) Label() string {
	switch f {
	case FromAddress:
		return "From"
	case FromDomain:
		return "From domain"
	case List:
		return "Mailing list"
	case ToAddress:
		return "To or Cc"
	case Subject:
		return "Subject contains"
	}
	return string(f)
}

// Rule files matching mail into a folder.
type Rule struct {
	Field    Field  `json:"field"`
	Value    string `json:"value"`
	Folder   string `json:"folder"` // the server's name, with its delimiter
	MarkRead bool   `json:"read,omitempty"`
}

func (r Rule) String() string {
	s := fmt.Sprintf("%s %s → %s", r.Field.Label(), r.Value, r.Folder)
	if r.MarkRead {
		s += ", marked read"
	}
	return s
}

// Valid says what is wrong with a rule, or "".
func (r Rule) Valid() string {
	switch {
	case strings.TrimSpace(r.Value) == "":
		return "the rule needs something to match"
	case strings.TrimSpace(r.Folder) == "":
		return "the rule needs a folder"
	case strings.ContainsAny(r.Value+r.Folder, "\r\n"):
		return "a rule can't contain a line break"
	}
	for _, f := range Fields {
		if r.Field == f {
			return ""
		}
	}
	return fmt.Sprintf("unknown field %q", r.Field)
}

const (
	header     = "# Mail filters written by foonmail. Edit them there: changes made\n# here by hand are kept, but foonmail then shows the script as text.\n"
	ruleMarker = "# rule: "
)

// Script writes rules as a Sieve script. Each rule is an if with stop, in
// order, so the first match wins; the JSON comment above each lets
// Parse read the rules back.
func Script(rules []Rule) string {
	var b strings.Builder
	b.WriteString(header)
	req := []string{`"fileinto"`, `"mailbox"`}
	for _, r := range rules {
		if r.MarkRead {
			req = append(req, `"imap4flags"`)
			break
		}
	}
	fmt.Fprintf(&b, "require [%s];\n", strings.Join(req, ", "))
	for _, r := range rules {
		j, _ := json.Marshal(r)
		fmt.Fprintf(&b, "\n%s%s\nif %s {\n", ruleMarker, j, test(r))
		if r.MarkRead {
			b.WriteString("    addflag \"\\\\Seen\";\n")
		}
		fmt.Fprintf(&b, "    fileinto :create %s;\n    stop;\n}\n", str(r.Folder))
	}
	return b.String()
}

func test(r Rule) string {
	v := str(strings.TrimSpace(r.Value))
	switch r.Field {
	case FromAddress:
		return `address :is "from" ` + v
	case FromDomain:
		return `address :domain :is "from" ` + v
	case List:
		return `header :contains "list-id" ` + v
	case ToAddress:
		return `address :is ["to", "cc"] ` + v
	default:
		return `header :contains "subject" ` + v
	}
}

// str quotes s as a Sieve string.
func str(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// Parse reads the rules back from a script Script wrote. byHand is true
// when the script isn't exactly what Script makes of those rules: it was
// written or changed elsewhere, so editing it as rules would lose that.
func Parse(script string) (rules []Rule, byHand bool) {
	for _, line := range strings.Split(script, "\n") {
		j, ok := strings.CutPrefix(strings.TrimSpace(line), ruleMarker)
		if !ok {
			continue
		}
		var r Rule
		if err := json.Unmarshal([]byte(j), &r); err != nil {
			return nil, true
		}
		rules = append(rules, r)
	}
	return rules, Script(rules) != script
}
