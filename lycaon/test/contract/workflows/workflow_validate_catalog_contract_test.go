package contract

import (
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/workflowvalidate"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestValidateCatalogBundledLibrary(t *testing.T) {
	t.Parallel()
	// The bundled catalog is read from disk, and an empty root resolves to
	// nothing from this package's directory.
	configRoot := filepath.Join(contractcheck.RepoRoot(t), "lycaon")
	reg, sources, err := workflowvalidate.CatalogSources(workflowvalidate.CatalogValidateOptions{
		ConfigRoot: configRoot,
		Mode:       workflowvalidate.ModeBundled,
	})
	contractcheck.FailErr(t, "CatalogSources", err)
	if len(sources) == 0 || len(reg.All()) == 0 {
		t.Fatal("expected bundled workflows")
	}
	if len(sources) != len(reg.All()) {
		t.Fatalf("source/registry bijection broken: sources=%d registry=%d", len(sources), len(reg.All()))
	}
	diags, err := workflowvalidate.ValidateCatalog(t.Context(), workflowvalidate.CatalogValidateOptions{
		ConfigRoot: configRoot,
		Mode:       workflowvalidate.ModeBundled,
	})
	contractcheck.FailErr(t, "ValidateCatalog", err)
	if len(diags) > 0 {
		t.Fatalf("bundled validate diagnostics: %v", diags)
	}
}
