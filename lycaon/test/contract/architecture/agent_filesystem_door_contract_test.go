package contract

import (
	"go/ast"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// agentAdapterPackages reach files on an agent's behalf. Their content reads
// and mutations go through ProgramPaths, projectpaths and fseffect, which hold
// the admitted object rather than reopening a pathname.
var agentAdapterPackages = []string{
	"internal/tools/native/",
	"internal/httpaction/",
	"internal/commandprogram/",
}

// rawFilesystemCalls reopen or change a pathname outside the door.
var rawFilesystemCalls = map[string]bool{
	"Open": true, "OpenFile": true, "ReadFile": true, "WriteFile": true, "Create": true, "CreateTemp": true,
	"Remove": true, "RemoveAll": true, "Rename": true, "Mkdir": true, "MkdirAll": true, "MkdirTemp": true,
	"Symlink": true, "Link": true, "Chmod": true, "Chown": true, "Lchown": true, "Chtimes": true,
	"Truncate": true, "ReadDir": true,
}

// agentFilesystemExemptions name each remaining raw call by file and function.
var agentFilesystemExemptions = map[string]string{
	"internal/tools/native/command/command_not_found.go:resolvableNeighbours": "lists PATH directories for command-not-found suggestions; names only, never project content",
	"internal/tools/native/gitcmd/remove.go:pruneEmptyParents":                "checks emptiness of directories inside the bound repository before the delete door removes them",
	"internal/tools/native/sourceview/rejects.go:PathNotFound":                "lists the parent of a resolved missing path for spelling suggestions",
}

func TestAgentAdaptersReachFilesThroughTheDoor(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	corp, err := contractcheck.LoadGoASTCorpus(filepath.Join(root, "lycaon"))
	contractcheck.FailErr(t, "load AST corpus", err)

	unused := map[string]bool{}
	for key := range agentFilesystemExemptions {
		unused[key] = true
	}
	var offences []string
	for _, gf := range corp.Files() {
		rel := filepath.ToSlash(gf.Rel)
		if gf.IsTest || !hasAnyPrefix(rel, agentAdapterPackages) {
			continue
		}
		osName := importName(gf.AST, "os")
		if osName == "" {
			continue
		}
		for _, decl := range gf.AST.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				pkg, ok := sel.X.(*ast.Ident)
				if !ok || pkg.Name != osName || !rawFilesystemCalls[sel.Sel.Name] {
					return true
				}
				key := rel + ":" + fn.Name.Name
				if _, exempt := agentFilesystemExemptions[key]; exempt {
					delete(unused, key)
					return true
				}
				offences = append(offences, key+" calls os."+sel.Sel.Name)
				return true
			})
		}
	}
	sort.Strings(offences)
	if len(offences) > 0 {
		t.Errorf("agent adapters must read and change files through ProgramPaths, projectpaths or fseffect, "+
			"which hold the admitted object instead of reopening a pathname:\n  %s", strings.Join(offences, "\n  "))
	}
	if len(unused) > 0 {
		var stale []string
		for key := range unused {
			stale = append(stale, key)
		}
		sort.Strings(stale)
		t.Errorf("these exemptions no longer match anything and should be deleted:\n  %s", strings.Join(stale, "\n  "))
	}
}

func hasAnyPrefix(s string, prefixes []string) bool {
	for _, prefix := range prefixes {
		if strings.HasPrefix(s, prefix) {
			return true
		}
	}
	return false
}

// importName is the local name of an import path in f, or "".
func importName(f *ast.File, path string) string {
	for _, spec := range f.Imports {
		if p, err := strconv.Unquote(spec.Path.Value); err != nil || p != path {
			continue
		}
		if spec.Name != nil {
			return spec.Name.Name
		}
		return filepath.Base(path)
	}
	return ""
}
