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

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// pathLookupOwners resolve programs for the processes they launch themselves.
var pathLookupOwners = []string{
	// Owns the resolved PATH every child starts from (exec.LookPath, exec.EffectivePathValue).
	"lycaon/internal/exec/",
	// The confinement helper runs inside the child's launch environment, so its
	// process PATH is the child's PATH.
	"lycaon/internal/confine/",
}

// Program lookups use the engine's resolved PATH. A GUI-launched engine
// inherits a minimal process PATH, so exec.LookPath or os.Getenv("PATH")
// report tools missing that every child the engine launches can run.
func TestProgramLookupsUseTheResolvedPath(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	internal := filepath.Join(root, "lycaon", "internal")
	fset := token.NewFileSet()
	var findings []string
	err := filepath.WalkDir(internal, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() {
			return walkErr
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)
		if ownsPathLookup(rel) {
			return nil
		}
		f, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			return parseErr
		}
		findings = append(findings, processPathLookups(fset, rel, f)...)
		return nil
	})
	contractcheck.FailErr(t, "scan program lookups", err)
	if len(findings) != 0 {
		sort.Strings(findings)
		t.Fatalf("program lookup searches the engine process PATH instead of the resolved PATH; "+
			"use exec.LookPath or exec.EffectivePathValue from internal/exec:\n  %s",
			strings.Join(findings, "\n  "))
	}
}

func ownsPathLookup(rel string) bool {
	for _, prefix := range pathLookupOwners {
		if strings.HasPrefix(rel, prefix) {
			return true
		}
	}
	return false
}

// processPathLookups finds any reference to os/exec.LookPath, called or passed
// as a value, and any read of the PATH variable through os.
func processPathLookups(fset *token.FileSet, rel string, f *ast.File) []string {
	aliases := importAliases(f)
	var found []string
	ast.Inspect(f, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.SelectorExpr:
			if pkg, ok := node.X.(*ast.Ident); ok && aliases[pkg.Name] == "os/exec" && node.Sel.Name == "LookPath" {
				found = append(found, positionFinding(fset, node.Pos(), rel, "os/exec.LookPath searches the process PATH"))
			}
		case *ast.CallExpr:
			sel, ok := node.Fun.(*ast.SelectorExpr)
			if !ok || len(node.Args) == 0 {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if !ok || aliases[pkg.Name] != "os" || (sel.Sel.Name != "Getenv" && sel.Sel.Name != "LookupEnv") {
				return true
			}
			if lit, ok := node.Args[0].(*ast.BasicLit); ok && lit.Kind == token.STRING && strings.Trim(lit.Value, "`\"") == "PATH" {
				found = append(found, positionFinding(fset, node.Pos(), rel, "os."+sel.Sel.Name+"(\"PATH\") reads the process PATH"))
			}
		}
		return true
	})
	return found
}
