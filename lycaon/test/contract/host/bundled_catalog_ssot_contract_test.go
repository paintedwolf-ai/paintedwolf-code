package contract

import (
	"os"
	"path/filepath"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/internal/workflowfixture"
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

func TestBundledCatalogDemoSKUs(t *testing.T) {
	t.Parallel()
	catalog, err := workflowfixture.LoadMergedWorkflowCatalog(t)
	contractcheck.FailErr(t, "loadMergedWorkflowCatalog", err)

	want := []string{
		"plan@1.0.0",
		"security-survey@1.0.0",
		"recon-pack@1.0.0",
		"bugbash@1.0.0",
		"options@1.0.0",
		"implement@1.0.0",
	}
	for _, key := range want {
		if _, ok := catalog[key]; !ok {
			t.Fatalf("bundled catalog missing %q", key)
		}
	}
}
