package contract

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
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
	openAPI, err := wirespec.LoadOpenAPIEnums(root)
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

func TestWebResearchCatalogDenGeneratedSync(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	cat, err := webresearch.LoadCatalog()
	contractcheck.FailErr(t, "LoadCatalog", err)

	denPath := filepath.Join(root, "lycaon-den", "src", "settings", "web-research-catalog.generated.ts")
	denData, err := os.ReadFile(denPath)
	contractcheck.FailErr(t, "read Den generated catalog", err)
	denText := string(denData)

	denIDs := parseDenProviderIDUnion(denText)
	yamlIDs := cat.IDs()
	sort.Strings(denIDs)
	sort.Strings(yamlIDs)
	if strings.Join(denIDs, ",") != strings.Join(yamlIDs, ",") {
		t.Fatalf("Den WebResearchProviderId union != YAML ids\nden:  %v\nyaml: %v", denIDs, yamlIDs)
	}

	denRoles := parseDenCatalogRoles(denText)
	for _, entry := range cat.Entries() {
		got, ok := denRoles[entry.ID]
		if !ok {
			t.Fatalf("Den WEB_RESEARCH_CATALOG missing entry %q", entry.ID)
		}
		want := append([]webresearch.ProviderRole(nil), entry.Roles...)
		if len(want) == 0 {
			want = []webresearch.ProviderRole{webresearch.RoleResults}
		}
		if !rolesEqual(got, want) {
			t.Fatalf("roles mismatch for %q: den=%v yaml=%v", entry.ID, got, want)
		}
		if entry.DefaultEnabled {
			if !strings.Contains(denText, `id: "`+entry.ID+`"`) {
				t.Fatalf("Den catalog missing id block for %q", entry.ID)
			}
			block := denEntryBlock(denText, entry.ID)
			if !strings.Contains(block, "default_enabled: true") {
				t.Fatalf("Den catalog %q missing default_enabled: true", entry.ID)
			}
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

var (
	reDenProviderIDUnion = regexp.MustCompile(`export type WebResearchProviderId = (.+);`)
	reDenCatalogEntry    = regexp.MustCompile(`(?s)\{\s*id:\s*"([^"]+)"[^}]*roles:\s*\[([^\]]*)\]`)
)

func parseDenProviderIDUnion(ts string) []string {
	m := reDenProviderIDUnion.FindStringSubmatch(ts)
	if len(m) < 2 {
		return nil
	}
	raw := m[1]
	parts := strings.Split(raw, "|")
	var ids []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		p = strings.Trim(p, `"`)
		if p != "" {
			ids = append(ids, p)
		}
	}
	return ids
}

func parseDenCatalogRoles(ts string) map[string][]webresearch.ProviderRole {
	out := make(map[string][]webresearch.ProviderRole)
	for _, m := range reDenCatalogEntry.FindAllStringSubmatch(ts, -1) {
		id := m[1]
		roleText := strings.TrimSpace(m[2])
		if roleText == "" {
			out[id] = nil
			continue
		}
		var roles []webresearch.ProviderRole
		for _, part := range strings.Split(roleText, ",") {
			part = strings.TrimSpace(part)
			part = strings.Trim(part, `"`)
			switch part {
			case "results":
				roles = append(roles, webresearch.RoleResults)
			case "seeds":
				roles = append(roles, webresearch.RoleSeeds)
			}
		}
		out[id] = roles
	}
	return out
}

func denEntryBlock(ts, id string) string {
	needle := `id: "` + id + `"`
	idx := strings.Index(ts, needle)
	if idx < 0 {
		return ""
	}
	start := strings.LastIndex(ts[:idx], "{")
	end := strings.Index(ts[idx:], "}")
	if start < 0 || end < 0 {
		return ts[idx:]
	}
	return ts[start : idx+end+1]
}

func rolesEqual(a, b []webresearch.ProviderRole) bool {
	if len(a) != len(b) {
		return false
	}
	aa := append([]webresearch.ProviderRole(nil), a...)
	bb := append([]webresearch.ProviderRole(nil), b...)
	sort.Slice(aa, func(i, j int) bool { return aa[i] < aa[j] })
	sort.Slice(bb, func(i, j int) bool { return bb[i] < bb[j] })
	for i := range aa {
		if aa[i] != bb[i] {
			return false
		}
	}
	return true
}
