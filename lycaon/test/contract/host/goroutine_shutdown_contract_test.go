package contract

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// goroutineShutdownPackages are scanned for go statements without wait-group pairing.
var goroutineShutdownPackages = []string{
	"internal/app",
	"internal/session",
	"internal/app/security",
	"internal/session/turnadmission",
}

// goroutineShutdownAllowlist permits fire-and-forget goroutines with documented rationale.
var goroutineShutdownAllowlist = map[string]string{
	"internal/coordinator/loopwake/host_turns.go":         "HostTurns registers bounded turns; WaitForAsyncTurns cancels and joins all registered work",
	"internal/coordinator/promptloop/batch.go":            "parallel tool batch — parent wg.Wait drains per turn",
	"internal/session/curation/service.go":                "curation runs drain on shutdown",
	"internal/app/runners.go":                             "startRunners pairs wg.Add with runner goroutines",
	"internal/app/serve.go":                               "Run uses http.Server.Shutdown for the listen goroutine",
	"internal/app/parent_watch.go":                        "watchParentExit — Run cancels the watch context and joins the returned channel before draining",
	"internal/app/security/evidence.go":                   "secret-harvest growth sweep is an event callback; the bound transcript.SweepSessionTree coalesces generations per root and finishes after screening durable rows",
	"internal/session/history/runner.go":                  "history.Runner.Wait drains Trigger goroutines on shutdown",
	"internal/session/turnadmission/queue_round_drain.go": "round-end drains register work with roundEndDrains; Service.Wait cancels and joins that work on shutdown",
}

func TestBackgroundGoroutinesPairedWithShutdown(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	fset := token.NewFileSet()
	var violations []string

	for _, pkg := range goroutineShutdownPackages {
		dir := filepath.Join(root, "lycaon", pkg)
		violations = append(violations, scanPackageGoroutines(fset, dir, pkg)...)
	}
	contractcheck.FailViolations(t, "unguarded go func() in shutdown-sensitive packages", violations)
}

func scanPackageGoroutines(fset *token.FileSet, dir, relPkg string) []string {
	var violations []string
	entries, err := os.ReadDir(dir)
	if err != nil {
		return []string{relPkg + ": " + err.Error()}
	}
	for _, ent := range entries {
		name := ent.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		rel := filepath.ToSlash(filepath.Join(relPkg, name))
		if _, allow := goroutineShutdownAllowlist[rel]; allow {
			continue
		}
		path := filepath.Join(dir, name)
		file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			violations = append(violations, rel+": parse error: "+err.Error())
			continue
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			violations = append(violations, scanFuncGoroutines(fset, rel, fn)...)
		}
	}
	return violations
}

func scanFuncGoroutines(fset *token.FileSet, relFile string, fn *ast.FuncDecl) []string {
	var violations []string
	hasWGAdd := funcBodyContainsWGAdd(fn.Body)
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		stmt, ok := n.(*ast.GoStmt)
		if !ok {
			return true
		}
		if hasWGAdd {
			return true
		}
		pos := fset.Position(stmt.Pos())
		violations = append(violations, relFile+":"+strconv.Itoa(pos.Line)+": "+fn.Name.Name+"() launches go func() without wg.Add pairing — register with ServeApp.registerRunner/startRunners or allowlist")
		return true
	})
	return violations
}

func funcBodyContainsWGAdd(body *ast.BlockStmt) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Add" {
			return true
		}
		if ident, ok := sel.X.(*ast.Ident); ok && ident.Name == "wg" {
			if len(call.Args) == 1 {
				if lit, ok := call.Args[0].(*ast.BasicLit); ok && lit.Kind == token.INT && lit.Value == "1" {
					found = true
					return false
				}
			}
		}
		return true
	})
	return found
}

func TestGoroutineShutdownAllowlistStale(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	fset := token.NewFileSet()
	var stale []string
	for rel := range goroutineShutdownAllowlist {
		path := filepath.Join(root, "lycaon", rel)
		file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			stale = append(stale, rel+" (missing file)")
			continue
		}
		hasGo := false
		ast.Inspect(file, func(n ast.Node) bool {
			if _, ok := n.(*ast.GoStmt); ok {
				hasGo = true
				return false
			}
			return true
		})
		if !hasGo {
			stale = append(stale, rel)
		}
	}
	if len(stale) > 0 {
		t.Fatalf("stale goroutineShutdownAllowlist entries (no go func() in file): %v", stale)
	}
}
