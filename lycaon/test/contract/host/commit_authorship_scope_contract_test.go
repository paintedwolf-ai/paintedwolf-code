package contract

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// commitStagingFile is the one place allowed to read whole-tree dirtiness,
// because it is the place that narrows the result to session authorship.
const commitStagingFile = "git_commit_staging.go"

// A commit's path set comes from what this session wrote, not from what differs
// from HEAD — a shared working tree holds other writers' work. The scan walks
// the package's call sites, so a new caller is covered without listing it.
func TestDirtyTreeReachesCommitsOnlyThroughAuthorship(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(contractcheck.RepoRoot(t), "lycaon", "internal", "tools", "native")
	fset := token.NewFileSet()
	entries, err := os.ReadDir(dir)
	contractcheck.FailErr(t, "read native tools package", err)
	files := make(map[string]*ast.File)
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		path := filepath.Join(dir, name)
		file, parseErr := parser.ParseFile(fset, path, nil, 0)
		contractcheck.FailErr(t, "parse native tools file "+name, parseErr)
		files[path] = file
	}
	if len(files) == 0 {
		t.Fatal("parsed zero packages — the walker is pointed at the wrong directory")
	}

	var offenders []string
	seenStagingCall := false
	for path, file := range files {
		base := filepath.Base(path)
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "ChangedPaths" {
				return true
			}
			if base == commitStagingFile {
				seenStagingCall = true
				return true
			}
			offenders = append(offenders,
				base+":"+strconv.Itoa(fset.Position(call.Pos()).Line))
			return true
		})
	}
	if !seenStagingCall {
		t.Fatalf("no ChangedPaths call in %s — the guard no longer watches anything", commitStagingFile)
	}
	if len(offenders) > 0 {
		sort.Strings(offenders)
		t.Fatalf("ChangedPaths read outside %s:\n  %s\n"+
			"Whole-tree dirtiness is not this session's work. Narrow through session "+
			"authorship in %s, or take an explicit caller-supplied path list.",
			commitStagingFile, strings.Join(offenders, "\n  "), commitStagingFile)
	}
}
