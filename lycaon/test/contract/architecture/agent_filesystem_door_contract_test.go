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
		for _, use := range agentFilesystemUses(gf.AST, rel) {
			if _, exempt := agentFilesystemExemptions[use.key]; exempt {
				delete(unused, use.key)
				continue
			}
			offences = append(offences, use.key+" uses os."+use.call)
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

// filesystemUse is one raw os call or function value, keyed by the file and
// the function (or "<package>" for a package-level declaration) holding it.
type filesystemUse struct{ key, call string }

// agentFilesystemUses finds every raw filesystem use in file: a call, a
// function value or a package-level closure, wherever it appears.
func agentFilesystemUses(file *ast.File, rel string) []filesystemUse {
	osName := importName(file, "os")
	if osName == "" {
		return nil
	}
	var uses []filesystemUse
	if osName == "." {
		return []filesystemUse{{key: rel + ":<package>", call: "dot import"}}
	}
	for _, decl := range file.Decls {
		owner := "<package>"
		if fn, ok := decl.(*ast.FuncDecl); ok {
			owner = fn.Name.Name
		}
		ast.Inspect(decl, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == osName && rawFilesystemCalls[sel.Sel.Name] {
				uses = append(uses, filesystemUse{key: rel + ":" + owner, call: sel.Sel.Name})
			}
			return true
		})
	}
	return uses
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
