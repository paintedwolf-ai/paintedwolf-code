package contract

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/guidance/feedback"
	"github.com/lycaon/lycaon/internal/workflow"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// Catalog revisions bind the captured unit bytes used by loaders.
// packRootWalkPrimitives enumerate pack directories.
var packRootWalkPrimitives = map[string]bool{
	"KindDirs":             true,
	"DiscoverEffective":    true,
	"DiscoverStock":        true,
	"DiscoverStockContent": true,
}

// packRootWalkAllowlist contains non-runtime enumeration sites.
var packRootWalkAllowlist = map[string]string{
	// Reserved shell includes have no unit id.
	"lycaon/internal/prompts/bundled_layout.go": "reserved `_` template includes have no unit id",
	// Persona contracts are definitions rather than contribution units.
	"lycaon/internal/prompts/persona_contract.go": "reserved persona-contract definition",
	// Website code generation reads the checkout.
	"lycaon/internal/website/customization.go": "website codegen projection",
	"lycaon/internal/website/sdk.go":           "website codegen projection",
	// Compose validation checks declared references.
	"lycaon/internal/workflowvalidate/checks_closure.go": "author-ref existence probe",
	// Test staging produces a resolved catalog.
	"lycaon/internal/testutil/extpackstest/catalog.go": "test catalog staging helper",
}

func TestPackContentLoadersCompileFromCatalogBytes(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	sites := scanPackRootWalkCallSites(t, filepath.Join(root, "lycaon"))
	var unexpected []string
	for site := range sites {
		if _, ok := packRootWalkAllowlist[site]; !ok {
			unexpected = append(unexpected, site)
		}
	}
	sort.Strings(unexpected)
	for _, u := range unexpected {
		t.Errorf("%s: walks pack directories after resolve — compile from the resolved catalog's captured unit bytes "+
			"(catalog.LoadedUnitIDs + UnitContent), or add the file to packRootWalkAllowlist with the reason it is not a loader", u)
	}
	for allowed, why := range packRootWalkAllowlist {
		if _, ok := sites[allowed]; !ok {
			t.Errorf("packRootWalkAllowlist entry %s (%s) no longer walks pack directories — drop the entry", allowed, why)
		}
	}
}

// Disabled gate feedback is absent from the resolved catalog.
func TestGateFeedbackLoadsFromCatalogUnits(t *testing.T) {
	t.Parallel()
	stock, err := extpacks.DiscoverStockContent()
	contractcheck.FailErr(t, "DiscoverStockContent", err)

	full := extpacks.Resolve(t.Context(), extpacks.ResolveInput{Packs: stock, Desired: extpacks.EmptyDesired()})
	catalog, err := feedback.LoadGateFeedbackCatalogWithCatalog(full)
	contractcheck.FailErr(t, "LoadGateFeedbackCatalogWithCatalog", err)
	ids := catalog.GateIDs()
	if len(ids) == 0 {
		t.Fatal("no gate feedback definitions in the stock catalog")
	}
	unitID := extpacks.GateFeedbackUnitIDPrefix + ids[0]
	if !full.HasLoaded(unitID) {
		t.Fatalf("gate feedback id %q has no provide unit %q — its bytes are outside the catalog revision", ids[0], unitID)
	}

	desired := extpacks.EmptyDesired()
	desired.Disabled = []string{unitID}
	reduced := extpacks.Resolve(t.Context(), extpacks.ResolveInput{Packs: stock, Desired: desired})
	after, err := feedback.LoadGateFeedbackCatalogWithCatalog(reduced)
	contractcheck.FailErr(t, "LoadGateFeedbackCatalogWithCatalog disabled", err)
	if after.Has(ids[0]) {
		t.Fatalf("disabled gate feedback %q still loaded — the loader is reading pack directories", ids[0])
	}
}

// Workflow manifests and templates come from the same unit algebra.
func TestWorkflowManifestsAndTemplatesLoadFromCatalogUnits(t *testing.T) {
	t.Parallel()
	stock, err := extpacks.DiscoverStockContent()
	contractcheck.FailErr(t, "DiscoverStockContent", err)
	full := extpacks.Resolve(t.Context(), extpacks.ResolveInput{Packs: stock, Desired: extpacks.EmptyDesired()})

	manifests, _, err := workflowdef.LoadManifestsFromCatalog(full)
	contractcheck.FailErr(t, "LoadManifestsFromCatalog", err)
	if len(manifests) == 0 {
		t.Fatal("no workflow manifests in the stock catalog")
	}
	templates, err := workflow.LoadTemplatesEffective(full)
	contractcheck.FailErr(t, "LoadTemplatesEffective", err)
	if len(templates) == 0 {
		t.Fatal("no workflow templates in the stock catalog")
	}

	// The template's unit id is its file stem, which need not equal its body id,
	// so take the id the catalog published rather than deriving one.
	var templateUnitID string
	for _, id := range full.LoadedUnitIDs() {
		if strings.HasPrefix(id, extpacks.WorkflowTemplateUnitIDPrefix) {
			templateUnitID = id
			break
		}
	}
	if templateUnitID == "" {
		t.Fatal("no workflow template provide units in the stock catalog")
	}
	desired := extpacks.EmptyDesired()
	desired.Disabled = []string{extpacks.WorkflowUnitID("plan"), templateUnitID}
	reduced := extpacks.Resolve(t.Context(), extpacks.ResolveInput{Packs: stock, Desired: desired})

	afterManifests, _, err := workflowdef.LoadManifestsFromCatalog(reduced)
	contractcheck.FailErr(t, "LoadManifestsFromCatalog disabled", err)
	if len(afterManifests) >= len(manifests) {
		t.Fatalf("disabling workflows/plan kept %d manifests (was %d) — the loader is reading pack directories",
			len(afterManifests), len(manifests))
	}
	afterTemplates, err := workflow.LoadTemplatesEffective(reduced)
	contractcheck.FailErr(t, "LoadTemplatesEffective disabled", err)
	if len(afterTemplates) >= len(templates) {
		t.Fatalf("disabling a template unit kept %d templates (was %d) — the loader is reading pack directories",
			len(afterTemplates), len(templates))
	}
}

// A nil catalog resolves committed device state; it never means "load everything".
func TestContributionLoadersRefuseNilCatalog(t *testing.T) {
	t.Parallel()
	if _, err := feedback.LoadGateFeedbackCatalogWithCatalog(nil); err == nil {
		t.Error("LoadGateFeedbackCatalogWithCatalog(nil) must fail closed")
	}
	if _, _, err := workflowdef.LoadManifestsFromCatalog(nil); err == nil {
		t.Error("LoadManifestsFromCatalog(nil) must fail closed")
	}
	if _, err := workflow.LoadTemplatesEffective(nil); err == nil {
		t.Error("LoadTemplatesEffective(nil) must fail closed")
	}
}

func scanPackRootWalkCallSites(t *testing.T, lycaonRoot string) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	err := filepath.WalkDir(lycaonRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case "testdata", "vendor", ".git", "test", "config":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, relErr := filepath.Rel(filepath.Dir(lycaonRoot), path)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)
		// extpacks performs discovery; it is the resolver, not a consumer.
		if strings.HasPrefix(rel, "lycaon/internal/extpacks/") {
			return nil
		}
		f, parseErr := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if parseErr != nil {
			return parseErr
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel == nil || !packRootWalkPrimitives[sel.Sel.Name] {
				return true
			}
			if ident, ok := sel.X.(*ast.Ident); ok && ident.Name == "extpacks" {
				out[rel] = true
			}
			return true
		})
		return nil
	})
	contractcheck.FailErr(t, "walk pack-root walk sites", err)
	return out
}
