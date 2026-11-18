package contract

import (
	"go/ast"
	"path/filepath"
	"strconv"
)

// rawFilesystemCalls reopen or change a pathname outside the door.
var rawFilesystemCalls = map[string]bool{
	"Open": true, "OpenFile": true, "ReadFile": true, "WriteFile": true, "Create": true, "CreateTemp": true,
	"Remove": true, "RemoveAll": true, "Rename": true, "Mkdir": true, "MkdirAll": true, "MkdirTemp": true,
	"Symlink": true, "Link": true, "Chmod": true, "Chown": true, "Lchown": true, "Chtimes": true,
	"Truncate": true, "ReadDir": true,
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
