package contract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/db"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestFreshWipeProductionGate(t *testing.T) {
	t.Setenv("LYCAON_DB_FRESH", "1")
	t.Setenv("LYCAON_CONFIG_DIR", "")
	t.Setenv("LYCAON_DEV", "")
	if db.FreshEnabled() {
		t.Fatal("LYCAON_DB_FRESH=1 must be ignored on the production channel")
	}
	if !db.FreshRequested() {
		t.Fatal("FreshRequested must still report the flag so serve can log the refusal")
	}
	t.Setenv("LYCAON_DEV", "1")
	if !db.FreshEnabled() {
		t.Fatal("development channel must honor LYCAON_DB_FRESH=1")
	}

	// The channel gate precedes the reset.
	root := contractcheck.RepoRoot(t)
	infraPath := filepath.Join(root, "lycaon", "internal", "app", "build_infra.go")
	raw, err := os.ReadFile(infraPath)
	contractcheck.FailErr(t, "read build_infra.go", err)
	src := string(raw)
	gate := strings.Index(src, "db.FreshEnabled()")
	wipe := strings.Index(src, "localdata.ResetStoreCoupled(")
	if gate == -1 || wipe == -1 || wipe < gate {
		t.Fatalf("build_infra.go must gate ResetStoreCoupled behind db.FreshEnabled (gate=%d wipe=%d)", gate, wipe)
	}
	if strings.Count(src, "localdata.ResetStoreCoupled(") != 1 {
		t.Fatal("build_infra.go must contain exactly one ResetStoreCoupled call, inside the fresh gate")
	}
}
