package contract

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// The ledger's hand-written CHECK constraint must match the state vocabulary.

func schemaCheckValues(t *testing.T, table, column string) []string {
	t.Helper()
	root := contractcheck.RepoRoot(t)
	body, err := os.ReadFile(filepath.Join(root, "lycaon", "internal", "db", "schema.sql"))
	contractcheck.FailErr(t, "read schema.sql", err)

	tableRe := regexp.MustCompile(`(?is)CREATE TABLE(?: IF NOT EXISTS)? ` + regexp.QuoteMeta(table) + ` \((.*?)\n\) STRICT`)
	match := tableRe.FindStringSubmatch(string(body))
	if match == nil {
		t.Fatalf("schema.sql declares no %s table", table)
	}
	checkRe := regexp.MustCompile(`(?is)` + regexp.QuoteMeta(column) + ` IN \(([^)]*)\)`)
	check := checkRe.FindStringSubmatch(match[1])
	if check == nil {
		t.Fatalf("%s.%s has no CHECK listing its values", table, column)
	}
	values := make([]string, 0, 4)
	for _, raw := range strings.Split(check[1], ",") {
		value := strings.Trim(strings.TrimSpace(raw), "'")
		if value != "" {
			values = append(values, value)
		}
	}
	sort.Strings(values)
	return values
}

// Only open, reopened, and fixed are stored; the other states are derived at read time.
func TestFindingLedgerStoresOnlyRecordedStates(t *testing.T) {
	t.Parallel()
	got := schemaCheckValues(t, "scan_finding_ledger", "state")
	want := []string{"fixed", "open", "reopened"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("scan_finding_ledger.state CHECK = %v, want only the recorded states %v", got, want)
	}
	for _, derived := range []api.FindingLedgerState{
		api.FindingLedgerNotObserved, api.FindingLedgerUnverified, api.FindingLedgerIgnored,
	} {
		for _, stored := range got {
			if stored == string(derived) {
				t.Fatalf("%q is derived from absence facts and must not be a stored state", derived)
			}
		}
	}
}
