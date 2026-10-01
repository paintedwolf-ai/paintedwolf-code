package contract

import (
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// Every durable write-root producer consults one refusal predicate.
func TestWriteRootProducerParity(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	internal := filepath.Join(root, "lycaon", "internal")
	consulted := map[string]bool{}
	var literalRoots []string

	corpus, err := contractcheck.LoadGoASTCorpus(internal)
	contractcheck.FailErr(t, "load internal Go corpus", err)
	for _, source := range corpus.Files() {
		if source.IsTest {
			continue
		}
		rel, _ := filepath.Rel(root, source.Path)
		pkgDir := filepath.Dir(rel)

		ast.Inspect(source.AST, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.Ident:
				// Call or function-value position both count: the predicate is
				// consulted either way.
				if node.Name == "AttachedWriteRootRefused" {
					consulted[pkgDir] = true
				}
			case *ast.CompositeLit:
				if confinementRootsHasStringLiteral(node) {
					pos := corpus.Fset.Position(node.Pos())
					literalRoots = append(literalRoots, rel+":"+strconv.Itoa(pos.Line))
				}
			}
			return true
		})
	}

	for _, gate := range []string{
		filepath.Join("lycaon", "internal", "confine"),
		filepath.Join("lycaon", "internal", "project"),
	} {
		if !consulted[gate] {
			t.Errorf("package %s must call AttachedWriteRootRefused — attach doors share one predicate", gate)
		}
	}

	confinePkg := filepath.Join("lycaon", "internal", "confine")
	for _, site := range literalRoots {
		if strings.HasPrefix(site, confinePkg+string(filepath.Separator)) {
			continue // Confinement defines its profile fixtures.
		}
		t.Errorf("%s: Confinement.Roots built from a string literal — durable roots must come through AttachedWriteRootRefused gates", site)
	}
}

func TestWriteRootChokePointsPinned(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	checks := []struct {
		rel  string
		want string
	}{
		{filepath.Join("lycaon", "internal", "project", "registry.go"), "AttachedWriteRootRefused"},
		{filepath.Join("lycaon", "internal", "session", "sandbox_write_root_broker.go"), "ControlPlanePathDenied"},
		// The grant door and the boundary read one granted-lane predicate: what
		// the broker grants as a plain root, prepareRequest applies.
		{filepath.Join("lycaon", "internal", "session", "sandbox_write_root_broker.go"), "GrantedWriteRootRefused"},
		{filepath.Join("lycaon", "internal", "confine", "request.go"), "ValidateGrantedWriteRoots"},
		{filepath.Join("lycaon", "internal", "confine", "write_root_grants.go"), "func AttachedWriteRootRefused"},
		{filepath.Join("lycaon", "internal", "confine", "write_root_grants.go"), "func GrantedWriteRootRefused"},
	}
	for _, c := range checks {
		raw, err := os.ReadFile(filepath.Join(root, c.rel))
		contractcheck.FailErr(t, "read "+c.rel, err)
		if !strings.Contains(string(raw), c.want) {
			t.Errorf("%s must contain %q", c.rel, c.want)
		}
	}
}

func confinementRootsHasStringLiteral(lit *ast.CompositeLit) bool {
	if !isConfinementType(lit.Type) {
		return false
	}
	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok || key.Name != "Roots" {
			continue
		}
		slice, ok := kv.Value.(*ast.CompositeLit)
		if !ok {
			continue
		}
		for _, e := range slice.Elts {
			if bl, ok := e.(*ast.BasicLit); ok && bl.Kind == token.STRING {
				return true
			}
		}
	}
	return false
}

func isConfinementType(expr ast.Expr) bool {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name == "Confinement"
	case *ast.SelectorExpr:
		return t.Sel != nil && t.Sel.Name == "Confinement"
	}
	return false
}

// confinementProducerInventory records each root producer and provenance.
var confinementProducerInventory = map[string]string{
	"internal/tools/native/command_exec.go":          "commandConfineRoots(tctx, extraWriteRoots) — host-selected root + attached roots + approved chat write-root overlay",
	"internal/tools/native/background_tools.go":      "commandConfineRoots(tctx, extraWriteRoots) — same union as foreground command",
	"internal/tools/native/terminal/session_open.go": "tools.ConfineRootsForAction(tctx) — host provenance only",
	"internal/tools/safecmd/safecmd.go":              "Confine(roots) wrapper; callers supply session roots (see internal/mcp/connector.go)",
	"internal/mcp/connector.go":                      "stdioConfinement(roots) — server spawn roots from the session, never from tool args",
	"internal/scan/bundled_confinement.go":           "BundledScannerConfinement(projectDir, outputDir) — bundled OpenGrep: project read-only plus exact host output root, network denied, mandatory application",
	"internal/confine/confine.go":                    "DefaultConfinement / BuildProfile themselves",
	"internal/hitl/contained.go":                     "ContainedForRequest projection — reads the same union the executor applies",
	"internal/tools/command_operand_expansion.go":    "globReadable(confReq) — pre-grant read boundaries for glob expansion",
}

// The producer inventory matches the syntax tree.
func TestConfinementProducerInventoryPinned(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	internal := filepath.Join(root, "lycaon", "internal")
	found := map[string]bool{}
	corpus, err := contractcheck.LoadGoASTCorpus(internal)
	contractcheck.FailErr(t, "load internal Go corpus", err)
	for _, source := range corpus.Files() {
		if source.IsTest {
			continue
		}
		rel, _ := filepath.Rel(filepath.Join(root, "lycaon"), source.Path)
		rel = filepath.ToSlash(rel)
		ast.Inspect(source.AST, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.CallExpr:
				if isConfinementProducerCall(node) {
					found[rel] = true
				}
			case *ast.CompositeLit:
				if isConfinementType(node.Type) {
					found[rel] = true
				}
			}
			return true
		})
	}

	for rel := range found {
		if _, ok := confinementProducerInventory[rel]; !ok {
			t.Errorf("%s builds a confinement but is not in confinementProducerInventory — "+
				"add it with its roots provenance, or route the roots through tools.ConfineRootsForAction", rel)
		}
	}
	for rel := range confinementProducerInventory {
		if !found[rel] {
			t.Errorf("confinementProducerInventory lists %s but no confinement producer was found there — "+
				"remove the stale entry", rel)
		}
	}
}

func isConfinementProducerCall(call *ast.CallExpr) bool {
	name := ""
	switch fun := call.Fun.(type) {
	case *ast.SelectorExpr:
		if fun.Sel != nil {
			name = fun.Sel.Name
		}
	case *ast.Ident:
		name = fun.Name
	}
	switch name {
	case "DefaultConfinement", "stdioConfinement":
		return true
	case "Confine":
		// Confine is the safe-command producer.
		return true
	}
	return false
}

// TestConfineRootsForActionTakesOnlyToolContext enforces a ToolContext-only input.
func TestConfineRootsForActionTakesOnlyToolContext(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	path := filepath.Join(root, "lycaon", "internal", "tools", "confine_roots.go")
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	contractcheck.FailErr(t, "parse confine_roots.go", err)

	var checked bool
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name == nil || fn.Name.Name != "ConfineRootsForAction" {
			continue
		}
		checked = true
		params := fn.Type.Params
		if params == nil || len(params.List) != 1 {
			t.Fatalf("ConfineRootsForAction must take exactly one parameter (a ToolContext); got %d", len(params.List))
		}
		ident, ok := params.List[0].Type.(*ast.Ident)
		if !ok || ident.Name != "ToolContext" {
			t.Fatalf("ConfineRootsForAction parameter must be ToolContext, got %s",
				types.ExprString(params.List[0].Type))
		}
	}
	if !checked {
		t.Fatal("ConfineRootsForAction not found in internal/tools/confine_roots.go")
	}
}

func TestConfineCoalesceNamesNoEcosystemCaches(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	dir := filepath.Join(root, "lycaon", "internal", "confine")
	forbidden := []string{"/pkg/mod/", ".npm", ".cargo", "GOPATH", "GOCACHE"}
	var hits []string
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() {
			return walkErr
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		// The bounded package-store exception is declared in one file; the
		// security contract holds it clear of executable install directories.
		if filepath.Base(path) == "package_cache_roots.go" {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		for i, line := range strings.Split(string(raw), "\n") {
			for _, needle := range forbidden {
				if strings.Contains(line, needle) {
					hits = append(hits, rel+":"+strconv.Itoa(i+1)+": "+strings.TrimSpace(line))
				}
			}
		}
		return nil
	})
	contractcheck.FailErr(t, "walk confine", err)
	if len(hits) > 0 {
		t.Fatalf("confine production code names an ecosystem cache layout:\n  %s", strings.Join(hits, "\n  "))
	}
}
