package contract

import (
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// reMattnImport / reSqlite3Driver enforce the single repo-wide SQLite driver.
var (
	reMattnImport   = regexp.MustCompile(`github\.com/mattn/go-sqlite3`)
	reSqlite3Driver = regexp.MustCompile(`sql\.Open\(\s*"sqlite3"`)
	// reSQLInSprintf flags a fmt.Sprintf format literal that builds SQL — value
	// interpolation into SQL is forbidden; bind with ? placeholders instead.
	reSQLInSprintf = regexp.MustCompile(`(?i)(select\s|insert\s+into|update\s|delete\s+from|\swhere\s|\svalues\s*\()`)
)

// TestSingleSQLiteDriver keeps the whole repo on the pure-Go modernc.org/sqlite
// driver ("sqlite"). No file may import mattn/go-sqlite3 or open the CGO
// "sqlite3" driver.
func TestSingleSQLiteDriver(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	lycaonDir := filepath.Join(root, "lycaon")
	var violations []string
	err := contractcheck.WalkFiles(lycaonDir, map[string]struct{}{".go": {}}, false, func(path string, data []byte) error {
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		// This enforcement file necessarily contains the forbidden literals in
		// its patterns and messages; skip scanning itself.
		if strings.HasSuffix(rel, "sql_query_conventions_contract_test.go") {
			return nil
		}
		lines := strings.Split(string(data), "\n")
		for i, line := range lines {
			if reMattnImport.MatchString(line) {
				violations = append(violations, rel+":"+strconv.Itoa(i+1)+": imports mattn/go-sqlite3 — use modernc.org/sqlite")
			}
			if reSqlite3Driver.MatchString(line) {
				violations = append(violations, rel+":"+strconv.Itoa(i+1)+`: sql.Open("sqlite3", …) — use the "sqlite" (modernc) driver`)
			}
		}
		return nil
	})
	contractcheck.FailErr(t, "walk lycaon for sqlite driver", err)
	if len(violations) > 0 {
		t.Fatalf("repo must use only modernc.org/sqlite:\n%s", strings.Join(violations, "\n"))
	}
}
