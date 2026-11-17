package contract

import (
	"os"
	"path/filepath"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// Desktop bundles rsync lycaon/config → engine-root at build time (scripts/den-build-bundle.sh).
// This contract pins the repo catalog SSOT.

func TestBundledCatalogHasOptionsWorkflow(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	dir := filepath.Join(root, "lycaon", "config", "packs", "painted-wolf", "options", "workflows", "options")
	if _, err := os.Stat(filepath.Join(dir, "workflow.yaml")); err != nil {
		t.Fatalf("options workflow missing from bundled catalog: %v", err)
	}
}
