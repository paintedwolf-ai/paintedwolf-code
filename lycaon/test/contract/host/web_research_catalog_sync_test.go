package contract

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/lycaon/lycaon/internal/webresearch"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/internal/wirespec"
)

// TestWebSearchProviderEnumCoversCatalog checks catalog and wire parity.
func TestWebSearchProviderEnumCoversCatalog(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	cat, err := webresearch.LoadCatalog()
	contractcheck.FailErr(t, "LoadCatalog", err)
	spec, err := wirespec.ReadSpec(root)
	contractcheck.FailErr(t, "read docs/openapi.yaml", err)
	openAPI, err := spec.Enums()
	contractcheck.FailErr(t, "load OpenAPI enums", err)
	want := append(cat.IDs(), "direct")
	contractcheck.FailSetEqual(t, "WebSearchProvider OpenAPI enum vs catalog ids + direct", want, openAPI["WebSearchProvider"])
}

func TestWebResearchCatalogRegistrySubset(t *testing.T) {
	cat, err := webresearch.LoadCatalog()
	contractcheck.FailErr(t, "LoadCatalog", err)

	reg := webresearch.NewRegistry(cat)
	contractcheck.FailErr(t, "RegisterCatalogProviders", webresearch.RegisterCatalogProviders(t.Context(), reg))

	for _, id := range cat.IDs() {
		entry, ok := cat.Entry(id)
		if !ok {
			t.Fatalf("catalog entry %q missing", id)
		}
		provider := reg.Get(id)
		if provider == nil {
			t.Fatalf("registry missing catalog provider %q", id)
		}
		if provider.Kind() != entry.Kind {
			t.Fatalf("kind mismatch for %q: registry=%q yaml=%q", id, provider.Kind(), entry.Kind)
		}
	}
}

// Provider behaviour lives in catalog specs and credential presence, never in a
// comparison or case against a provider id.
func TestWebResearchNoProviderIDBehaviorBranches(t *testing.T) {
	t.Parallel()
	cat, err := webresearch.LoadCatalog()
	contractcheck.FailErr(t, "LoadCatalog", err)
	ids := map[string]bool{}
	for _, id := range cat.IDs() {
		ids[id] = true
	}
	if len(ids) == 0 {
		t.Fatal("catalog has no provider ids")
	}
	dir := filepath.Join(contractcheck.RepoRoot(t), "lycaon", "internal", "webresearch")
	corpus, err := contractcheck.LoadGoASTCorpusMode(dir, parser.SkipObjectResolution)
	contractcheck.FailErr(t, "load webresearch corpus", err)
	files := corpus.Production()
	if len(files) == 0 {
		t.Fatal("webresearch has no production sources")
	}
	var violations []string
	report := func(expr ast.Expr) {
		lit, ok := expr.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return
		}
		if value, err := strconv.Unquote(lit.Value); err == nil && ids[value] {
			violations = append(violations, fmt.Sprintf("%s: branch on provider id %q", corpus.Fset.Position(lit.Pos()), value))
		}
	}
	for _, file := range files {
		ast.Inspect(file.AST, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.BinaryExpr:
				if node.Op == token.EQL || node.Op == token.NEQ {
					report(node.X)
					report(node.Y)
				}
			case *ast.CaseClause:
				for _, expr := range node.List {
					report(expr)
				}
			}
			return true
		})
	}
	contractcheck.FailViolations(t, "provider-id behaviour branches in webresearch", violations)
}
