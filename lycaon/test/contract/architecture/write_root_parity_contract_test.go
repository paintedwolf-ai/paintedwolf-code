package contract

import (
	"go/ast"
	"go/token"
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
	} {
		if !consulted[gate] {
			t.Errorf("package %s must call AttachedWriteRootRefused — execution boundaries share one predicate", gate)
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
	"internal/tools/native/command/program_external.go":     "programEnvironment — DefaultConfinement from ActionConfineInputsForContext; host-derived roots and grants for the shared launch environment",
	"internal/tools/native/command/command_exec.go":         "commandConfineRoots(tctx, extraWriteRoots) — host-selected root + attached roots + approved chat write-root overlay",
	"internal/tools/native/terminal/session_open.go":        "tools.ConfineRootsForAction(tctx) — host provenance only",
	"internal/tools/safecmd/safecmd.go":                     "Confine(roots) wrapper; callers supply session roots (see internal/mcp/connector.go)",
	"internal/mcp/connector.go":                             "stdioConfinement(roots) — server spawn roots from the session, never from tool args",
	"internal/scan/bundled_confinement.go":                  "BundledScannerConfinement(projectDir, outputDir) — bundled OpenGrep: project read-only plus exact host output root, network denied, mandatory application",
	"internal/confine/confine.go":                           "DefaultConfinement / BuildSeatbeltProfile themselves",
	"internal/hitl/contained.go":                            "ContainedForRequest projection — reads the same union the executor applies",
	"internal/toolexecution/command_operand_expansion.go":   "globReadable(confReq) — pre-grant read boundaries for glob expansion",
	"internal/documentcore/process.go":                      "DefaultConfinement(host MkdirTemp scratch) — native document core: only its own scratch root, network denied, mandatory application",
	"internal/visualscreen/ocr_tesseract.go":                "DefaultConfinement(host MkdirTemp scratch) — bundled OCR engine: only its own per-image scratch root, network denied, mandatory application",
	"internal/layeredfs/adapter/viewconfine/viewconfine.go": "workerBox(view roots) — a worker's shape, to prove denial; host-derived roots only, network denied",
	"internal/exec/prepared.go":                             "Confinement{HostExecution: true} — no roots; the explicit host disposition when a launch plan carries no agent boundary, never replacing one it does carry",
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
