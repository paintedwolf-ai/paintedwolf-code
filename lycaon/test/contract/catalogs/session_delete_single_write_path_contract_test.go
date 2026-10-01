package contract

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// Session deletion has one write path because it spans rows without shared foreign keys.
const sessionDeleteWritePath = "lycaon/internal/db/session_delete.go"

// sessionDeleteStatements match whole-session deletion statements.
var sessionDeleteStatements = []*regexp.Regexp{
	regexp.MustCompile(`(?i)DELETE\s+FROM\s+sessions\s+WHERE`),
	regexp.MustCompile(`(?i)DELETE\s+FROM\s+evidence_index\s+WHERE\s+session_id\s*=`),
}

// sessionDeleteClients must call the shared write path.
var sessionDeleteClients = []string{
	"lycaon/internal/session/store/sql_sessions.go",
}

func TestSessionDeleteHasOneWritePath(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	var offenders []string

	for _, dir := range []string{
		filepath.Join(root, "lycaon", "internal"),
		filepath.Join(root, "lycaon", "pkg"),
	} {
		err := filepath.WalkDir(dir, func(path string, d os.DirEntry, walkErr error) error {
			if walkErr != nil || d.IsDir() {
				return walkErr
			}
			name := d.Name()
			if !strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, ".sql") {
				return nil
			}
			if strings.HasSuffix(name, "_test.go") {
				return nil
			}
			rel := filepath.ToSlash(mustRel(t, root, path))
			if rel == sessionDeleteWritePath {
				return nil
			}
			src, readErr := os.ReadFile(path)
			if readErr != nil {
				return readErr
			}
			for _, re := range sessionDeleteStatements {
				if re.Match(src) {
					offenders = append(offenders, rel+": "+re.String())
				}
			}
			return nil
		})
		contractcheck.FailErr(t, "walk "+dir, err)
	}

	if len(offenders) > 0 {
		t.Fatalf("session delete has more than one write path — route these through db.DeleteSessionTree in %s:\n  %s",
			sessionDeleteWritePath, strings.Join(offenders, "\n  "))
	}
}

func TestSessionDeleteClientsUseWritePath(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	for _, rel := range sessionDeleteClients {
		src, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		contractcheck.FailErr(t, "read "+rel, err)
		if !strings.Contains(string(src), "DeleteSessionTree(") {
			t.Fatalf("%s deletes sessions without calling db.DeleteSessionTree", rel)
		}
	}
}

func mustRel(t *testing.T, root, path string) string {
	t.Helper()
	rel, err := filepath.Rel(root, path)
	contractcheck.FailErr(t, "relativize "+path, err)
	return rel
}
