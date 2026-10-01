package contract

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// The full-debug setting enables every capture through debugpaths.Enabled.

const (
	observabilityPkgRel = "lycaon/internal/observability"
	debugpathsPkgRel    = "lycaon/internal/debugpaths"
)

// TestDebugGatesDelegateToChokepoint asserts every *DebugEnabled gate in the
// observability package delegates to debugpaths.Enabled, and that the
// chokepoint itself still consults the main switch. A gate that reads
// os.Getenv directly would silently escape LYCAON_DEBUG_ALL.
func TestDebugGatesDelegateToChokepoint(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)

	chokepointHonorsMain := false
	_, catalogFiles := contractcheck.ParseNonTestGoTree(t, filepath.Join(root, debugpathsPkgRel))
	for _, f := range catalogFiles {
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if ok && fn.Recv == nil && fn.Name.Name == "Enabled" {
				chokepointHonorsMain = bodyCallsAnyFunc(fn.Body, "FullDebugEnabled")
			}
		}
	}
	if !chokepointHonorsMain {
		t.Fatal("debugpaths.Enabled must call FullDebugEnabled so LYCAON_DEBUG_ALL enables every capture")
	}

	_, files := contractcheck.ParseNonTestGoTree(t, filepath.Join(root, observabilityPkgRel))
	var offenders []string
	gates := map[string]bool{}
	for _, f := range files {
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil || !strings.HasSuffix(fn.Name.Name, "DebugEnabled") {
				continue
			}
			gates[fn.Name.Name] = true
			if !bodyCallsSelector(fn.Body, "debugpaths", "Enabled") {
				offenders = append(offenders, fn.Name.Name)
			}
		}
	}
	sort.Strings(offenders)
	if len(offenders) > 0 {
		t.Fatalf(`debug capture gates must delegate to debugpaths.Enabled so LYCAON_DEBUG_ALL reaches them:
%s
Fix: return debugpaths.Enabled(debugpaths.Kind...) instead of reading os.Getenv directly.`,
			strings.Join(offenders, "\n"))
	}
	// A scan that finds no gates is vacuous.
	if len(gates) < 4 {
		t.Fatalf("expected several *DebugEnabled gates in internal/observability, found %d: %v",
			len(gates), sortedKeys(gates))
	}
}

// TestNoDirectDebugToggleGetenv asserts no file outside the debugpaths catalog
// reads a LYCAON_*_DEBUG toggle or the main switch via os.Getenv directly. A
// direct read would bypass the main switch even if it were wrapped in a helper.
func TestNoDirectDebugToggleGetenv(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	codeRoot := filepath.Join(root, "lycaon")

	var offenders []string
	scanned := 0
	err := contractcheck.WalkFiles(codeRoot, map[string]struct{}{".go": {}}, true, func(path string, data []byte) error {
		if strings.Contains(path, debugpathsPkgRel) {
			return nil
		}
		scanned++
		file, parseErr := parser.ParseFile(token.NewFileSet(), path, data, 0)
		if parseErr != nil {
			return parseErr
		}
		consts := stringConsts(file)
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || !isSelectorCall(call, "os", "Getenv") || len(call.Args) != 1 {
				return true
			}
			var env string
			switch a := call.Args[0].(type) {
			case *ast.BasicLit:
				env = strings.Trim(a.Value, "`\"")
			case *ast.Ident:
				env = consts[a.Name]
			}
			if env == "LYCAON_DEBUG_ALL" || (strings.HasPrefix(env, "LYCAON_") && strings.HasSuffix(env, "_DEBUG")) {
				rel, _ := filepath.Rel(root, path)
				offenders = append(offenders, fmt.Sprintf("%s: os.Getenv(%q)", rel, env))
			}
			return true
		})
		return nil
	})
	contractcheck.FailErr(t, "walk Go for debug toggle reads", err)
	if scanned == 0 {
		t.Fatal("scan visited no production Go files — this guard is inert")
	}
	sort.Strings(offenders)
	if len(offenders) > 0 {
		t.Fatalf(`debug toggles must be read via debugpaths.Enabled / debugpaths.FullDebugEnabled, not os.Getenv directly:
%s`, strings.Join(offenders, "\n"))
	}
}

// stringConsts resolves a file's string constants so os.Getenv(constName) can
// be checked, not just os.Getenv("literal").
func stringConsts(file *ast.File) map[string]string {
	consts := map[string]string{}
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, nm := range vs.Names {
				if i < len(vs.Values) {
					if v := contractcheck.AstStringLit(vs.Values[i]); v != "" {
						consts[nm.Name] = v
					}
				}
			}
		}
	}
	return consts
}

func bodyCallsAnyFunc(body *ast.BlockStmt, names ...string) bool {
	if body == nil {
		return false
	}
	want := make(map[string]bool, len(names))
	for _, n := range names {
		want[n] = true
	}
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		if found {
			return false
		}
		if call, ok := n.(*ast.CallExpr); ok {
			if id, ok := call.Fun.(*ast.Ident); ok && want[id.Name] {
				found = true
				return false
			}
		}
		return true
	})
	return found
}

func bodyCallsSelector(body *ast.BlockStmt, pkg, fn string) bool {
	if body == nil {
		return false
	}
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		if found {
			return false
		}
		if call, ok := n.(*ast.CallExpr); ok && isSelectorCall(call, pkg, fn) {
			found = true
			return false
		}
		return true
	})
	return found
}

func isSelectorCall(call *ast.CallExpr, pkg, fn string) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != fn {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	return ok && id.Name == pkg
}
