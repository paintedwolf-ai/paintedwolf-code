package secretmint

import (
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func testInspector(t *testing.T) *Inspector {
	t.Helper()
	ins, err := LoadBundled()
	testutil.FailErr(t, "LoadBundled", err)
	return ins
}

func assignments(hits []Candidate) []string {
	out := make([]string, len(hits))
	for i, h := range hits {
		out[i] = h.Assignment
	}
	return out
}

func hasAssignment(hits []Candidate, name string) bool {
	for _, h := range hits {
		if h.Assignment == name {
			return true
		}
	}
	return false
}

func TestInspectHtpasswdMintHits(t *testing.T) {
	ins := testInspector(t)
	hits := ins.Inspect("command", map[string]any{"command": "htpasswd -b user password"})
	if !hasAssignment(hits, "htpasswd") {
		t.Fatalf("htpasswd mint must hit, assignments=%v hit=%v", assignments(hits), len(hits) > 0)
	}
}

func TestInspectMySQLUseSilent(t *testing.T) {
	ins := testInspector(t)
	hits := ins.Inspect("command", map[string]any{"command": "mysql -p password"})
	if len(hits) != 0 {
		t.Fatalf("mysql -p is use, not mint: assignments=%v", assignments(hits))
	}
}

func TestInspectHydraAttackSilent(t *testing.T) {
	ins := testInspector(t)
	hits := ins.Inspect("command", map[string]any{"command": "hydra -P rockyou.txt ssh://example.test"})
	if len(hits) != 0 {
		t.Fatalf("hydra dictionary is attack, not mint: assignments=%v", assignments(hits))
	}
}

// A listed value is matched whole: containing a listed word is not being one.
func TestInspectListedWordInsideStrongValueIsNotListed(t *testing.T) {
	ins := testInspector(t)
	hits := ins.Inspect("command", map[string]any{"command": "htpasswd -b user K7password93XmQz2Lw"})
	if len(hits) != 1 || hits[0].Measurements().Listed {
		t.Fatalf("literal candidate must be present and not listed: %#v", hits)
	}
}

// Sixteen lowercase letters of ordinary words: on no list, still guessable.
func TestInspectGuessableValueHitsWithoutTheList(t *testing.T) {
	ins := testInspector(t)
	hits := ins.Inspect("command", map[string]any{"command": "htpasswd -b user mypasswordpolicy"})
	if !hasAssignment(hits, "htpasswd") {
		t.Fatalf("guessable mint must hit: assignments=%v", assignments(hits))
	}
	if hits[0].Measurements().Listed || hits[0].Measurements().Length != 16 {
		t.Fatalf("unexpected measurements: %#v", hits[0])
	}
}

func TestInspectQuoteStripAndCaseFold(t *testing.T) {
	ins := testInspector(t)
	quoted := ins.Inspect("command", map[string]any{"command": `htpasswd -b user "password"`})
	if !hasAssignment(quoted, "htpasswd") {
		t.Fatalf("quoted mint must hit: assignments=%v", assignments(quoted))
	}
	folded := ins.Inspect("command", map[string]any{"command": "htpasswd -b user Password"})
	if !hasAssignment(folded, "htpasswd") {
		t.Fatalf("case-fold mint must hit: assignments=%v", assignments(folded))
	}
}

func TestInspectUseraddPasswordFlag(t *testing.T) {
	ins := testInspector(t)
	hits := ins.Inspect("command", map[string]any{"command": "useradd -p password alice"})
	if !hasAssignment(hits, "-p") {
		t.Fatalf("useradd -p must hit: assignments=%v", assignments(hits))
	}
}

func TestInspectLeadingEnvAndStructuredEnv(t *testing.T) {
	ins := testInspector(t)
	leading := ins.Inspect("command", map[string]any{"command": "MYSQL_PASSWORD=password mysql"})
	if !hasAssignment(leading, "MYSQL_PASSWORD") {
		t.Fatalf("leading env mint must hit: assignments=%v", assignments(leading))
	}
	mapped := ins.Inspect("command", map[string]any{
		"command": "true",
		"env":     map[string]any{"MYSQL_PASSWORD": "password"},
	})
	if !hasAssignment(mapped, "MYSQL_PASSWORD") {
		t.Fatalf("structured env mint must hit: assignments=%v", assignments(mapped))
	}
}

func TestInspectSQLIdentifiedBy(t *testing.T) {
	ins := testInspector(t)
	hits := ins.Inspect("command", map[string]any{
		"command": `mysql -e "ALTER USER u IDENTIFIED BY 'password'"`,
	})
	if !hasAssignment(hits, "IDENTIFIED BY") {
		t.Fatalf("SQL mint must hit: assignments=%v", assignments(hits))
	}
}

func TestInspectTerminalSend(t *testing.T) {
	ins := testInspector(t)
	hits := ins.Inspect("terminal_send", map[string]any{"input": "htpasswd -b user password\n"})
	if !hasAssignment(hits, "htpasswd") {
		t.Fatalf("terminal_send mint must hit: assignments=%v", assignments(hits))
	}
}

func TestHtpasswdWithoutBatchFlagSilent(t *testing.T) {
	ins := testInspector(t)
	hits := ins.Inspect("command", map[string]any{"command": "htpasswd user password"})
	if len(hits) != 0 {
		t.Fatalf("htpasswd without -b is not a line mint: assignments=%v", assignments(hits))
	}
}
