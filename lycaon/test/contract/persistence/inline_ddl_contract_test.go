package contract

import (
	"path/filepath"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestNoInlineDDLInInternalDB(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	dbDir := filepath.Join(root, "lycaon", "internal", "db")

	violations, err := scanInternalDBInlineDDL(dbDir)
	contractcheck.FailErr(t, "scan internal/db for inline DDL", err)
	if len(violations) > 0 {
		t.Fatalf("internal/db production Go must not contain inline schema DDL (edit schema.sql instead):\n%s",
			strings.Join(violations, "\n"))
	}
}
