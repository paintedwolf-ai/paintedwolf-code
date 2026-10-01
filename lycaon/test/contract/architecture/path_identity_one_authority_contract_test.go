package contract

import (
	"go/ast"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// classificationAuthorities make path permission decisions.
var classificationAuthorities = []string{
	"github.com/lycaon/lycaon/internal/sensitivepath",
	"github.com/lycaon/lycaon/internal/settingsoverlay",
	"github.com/lycaon/lycaon/internal/grantedpath",
}

// pathIdentityExemptions allow uses that reject symlink components.
var pathIdentityExemptions = map[string]string{
	"internal/project/source_lifecycle_path.go": "lifecycle writes reject dangling parent links and retain the selected directory entry",
	"internal/fseffect/transaction_unix.go":     "atomic replace refuses symlink components rather than following them",
	"internal/fseffect/transaction_windows.go":  "atomic replace refuses symlink components rather than following them",
}

func TestPathIdentityHasOneAuthority(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	corp, err := contractcheck.LoadGoASTCorpus(filepath.Join(root, "lycaon"))
	contractcheck.FailErr(t, "load AST corpus", err)

	unusedExemption := map[string]bool{}
	for rel := range pathIdentityExemptions {
		unusedExemption[rel] = true
	}

	var decisionOffences, walkerOffences []string
	for _, gf := range corp.Files() {
		if gf.IsTest {
			continue
		}
		rel := filepath.ToSlash(gf.Rel)
		if strings.HasPrefix(rel, "internal/fspath/") {
			continue // the authority itself
		}
		if !fileCallsEvalSymlinks(gf.AST) {
			continue
		}
		if _, exempt := pathIdentityExemptions[rel]; exempt {
			delete(unusedExemption, rel)
			continue
		}
		if fileImportsAny(gf.AST, classificationAuthorities) {
			decisionOffences = append(decisionOffences, rel)
		}
		if fileResolvesByClimbing(gf.AST) {
			walkerOffences = append(walkerOffences, rel)
		}
	}

	sort.Strings(decisionOffences)
	sort.Strings(walkerOffences)

	if len(decisionOffences) > 0 {
		t.Errorf("a file that consults a classification authority must not resolve paths itself.\n"+
			"Use fspath.CanonicalPath in:\n  %s\n\n"+
			"filepath.EvalSymlinks cannot see the /System/Volumes/Data firmlink or a case-insensitive "+
			"volume, so one location keeps two names and only the name someone thought of classifies.",
			strings.Join(decisionOffences, "\n  "))
	}
	if len(walkerOffences) > 0 {
		t.Errorf("Use fspath.CanonicalPath to resolve missing path tails in:\n  %s",
			strings.Join(walkerOffences, "\n  "))
	}
	if len(unusedExemption) > 0 {
		var stale []string
		for rel := range unusedExemption {
			stale = append(stale, rel)
		}
		sort.Strings(stale)
		t.Errorf("these exemptions no longer match anything and should be deleted:\n  %s",
			strings.Join(stale, "\n  "))
	}
}

// fileCallsEvalSymlinks reports a filepath.EvalSymlinks call anywhere in f.
func fileCallsEvalSymlinks(f *ast.File) bool {
	found := false
	ast.Inspect(f, func(n ast.Node) bool {
		if found {
			return false
		}
		if isEvalSymlinksCall(n) {
			found = true
			return false
		}
		return true
	})
	return found
}

// fileResolvesByClimbing detects a local resolve-and-parent-walk implementation.
func fileResolvesByClimbing(f *ast.File) bool {
	found := false
	ast.Inspect(f, func(n ast.Node) bool {
		if found {
			return false
		}
		var body ast.Node
		switch loop := n.(type) {
		case *ast.ForStmt:
			body = loop.Body
		case *ast.RangeStmt:
			body = loop.Body
		default:
			return true
		}
		resolves, climbs := false, false
		ast.Inspect(body, func(inner ast.Node) bool {
			if isEvalSymlinksCall(inner) {
				resolves = true
			}
			if isFilepathCall(inner, "Dir") {
				climbs = true
			}
			return true
		})
		if resolves && climbs {
			found = true
			return false
		}
		return true
	})
	return found
}

func isFilepathCall(n ast.Node, name string) bool {
	call, ok := n.(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != name {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "filepath"
}

func isEvalSymlinksCall(n ast.Node) bool {
	return isFilepathCall(n, "EvalSymlinks")
}

func fileImportsAny(f *ast.File, paths []string) bool {
	for _, imp := range f.Imports {
		if imp.Path == nil {
			continue
		}
		got := strings.Trim(imp.Path.Value, `"`)
		for _, want := range paths {
			if got == want {
				return true
			}
		}
	}
	return false
}

// TestPathIdentityAuthorityIsReachable keeps the authority scan non-vacuous.
func TestPathIdentityAuthorityIsReachable(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	corp, err := contractcheck.LoadGoASTCorpus(filepath.Join(root, "lycaon", "internal", "fspath"))
	contractcheck.FailErr(t, "load fspath corpus", err)

	for _, gf := range corp.Files() {
		for _, decl := range gf.AST.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if ok && fn.Recv == nil && fn.Name.Name == "CanonicalPath" {
				return
			}
		}
	}
	t.Fatal("internal/fspath no longer exports CanonicalPath; " +
		"TestPathIdentityHasOneAuthority names it as the replacement and would now be vacuous")
}
