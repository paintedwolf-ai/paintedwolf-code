package contract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/anchorcatalog"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/oar"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// TestOARNextStepsArgsSchemaCheckedAtLoad loads the stock policy through a
// loader wired with the declared tool schemas, so every next-steps args and
// pick path is checked against the receiving tool's argument schema.
func TestOARNextStepsArgsSchemaCheckedAtLoad(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	contractcheck.FailErr(t, "install catalog", anchorcatalog.InstallFile(filepath.Join(root,
		"lycaon", "config", "packs", "painted-wolf", "platform", "host", "anchors", "catalog.yaml")))
	eff := contractcheck.StockCatalog(t)
	schemas, _, err := extpacks.LoadEffectiveToolSchemas(eff)
	contractcheck.FailErr(t, "load tool schemas", err)
	loader, err := oar.NewLoader(filepath.Join(root, "schemas"))
	contractcheck.FailErr(t, "oar loader", err)
	loader.SetToolSchemas(schemas)
	if _, err := loader.LoadEffectivePolicyWithCatalog(eff); err != nil {
		t.Fatalf("stock policy must load with args schema-checked: %v", err)
	}
}

// TestOARCatalogViewWiresToolSchemas pins the production wiring: the catalog
// view installs tool schemas on the loader before compiling rules, so the
// load-time args check runs on every boot path that renders next steps.
func TestOARCatalogViewWiresToolSchemas(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile(filepath.Join(contractcheck.RepoRoot(t),
		"lycaon", "internal", "catalogview", "view.go"))
	contractcheck.FailErr(t, "read catalogview/view.go", err)
	src := string(raw)
	set := strings.Index(src, "loader.SetToolSchemas(")
	load := strings.Index(src, "loader.LoadEffectivePolicyEntries(")
	if set < 0 || load < 0 {
		t.Fatalf("catalog view must install tool schemas and compile rules; found set=%d load=%d", set, load)
	}
	if set > load {
		t.Fatal("tool schemas must be installed before rule compilation so next-steps args are checked at load")
	}
}
