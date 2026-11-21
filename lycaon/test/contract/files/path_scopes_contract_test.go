package contract

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/sandbox"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestCoordinatorProfileUsesProductReadScope(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "lycaon", "config", "packs", "painted-wolf", "platform", "tools", "profiles", "coordinator.yaml"))
	contractcheck.FailErr(t, "read coordinator profile", err)

	scopes, err := sandbox.LoadPathScopes()
	contractcheck.FailErr(t, "load path-scopes.yaml", err)
	read, ok := scopes["coordinator_product_read"]
	if !ok || len(read.Read) == 0 {
		t.Fatal("path scope coordinator_product_read declares no read globs")
	}
	write, ok := scopes["coordinator_orchestration"]
	if !ok || len(write.Write) == 0 {
		t.Fatal("path scope coordinator_orchestration declares no write globs")
	}

	prof, err := sandbox.ParseToolProfile(data, scopes)
	contractcheck.FailErr(t, "parse coordinator profile", err)
	if !slices.Equal(prof.ReadGlobs, read.Read) {
		t.Fatalf("resolved read_globs = %v want %v", prof.ReadGlobs, read.Read)
	}
	if !slices.Equal(prof.WriteGlobs, write.Write) {
		t.Fatalf("resolved write_globs = %v want %v", prof.WriteGlobs, write.Write)
	}
}
