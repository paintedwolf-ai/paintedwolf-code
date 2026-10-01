package native

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// bannedMutations require write-door attribution.
var bannedMutations = map[string][]string{
	"os":       {"Rename", "Remove", "RemoveAll", "WriteFile", "Create", "OpenFile", "Truncate", "Mkdir", "MkdirAll", "Chmod", "Chown", "Lchown"},
	"fseffect": {"Replace", "Remove", "Rename", "MkdirAll", "OpenWrite", "Chmod", "Chown"},
}

func TestWorkspaceContentReadsUseDescriptorRelativeOpen(t *testing.T) {
	var offences []string
	fset := token.NewFileSet()
	for _, name := range nativeProductionFiles(t) {
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || (sel.Sel.Name != "Open" && sel.Sel.Name != "ReadFile") {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if ok && pkg.Name == "os" {
				offences = append(offences, fmt.Sprintf("%s:%d: os.%s", name, fset.Position(call.Pos()).Line, sel.Sel.Name))
			}
			return true
		})
	}
	if len(offences) != 0 {
		sort.Strings(offences)
		t.Fatalf("workspace content read outside fseffect.OpenRead:\n  %s", strings.Join(offences, "\n  "))
	}
}

// doorFile records attributed workspace mutations.
const doorFile = "source_write.go"

func TestWorkspaceMutationsGoThroughTheWriteDoor(t *testing.T) {
	var offences []string
	fset := token.NewFileSet()
	for _, name := range nativeProductionFiles(t) {
		if name == doorFile {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			for _, fn := range bannedMutations[pkg.Name] {
				if sel.Sel.Name == fn {
					offences = append(offences, fmt.Sprintf("%s:%d: %s.%s",
						name, fset.Position(call.Pos()).Line, pkg.Name, fn))
				}
			}
			return true
		})
	}
	if len(offences) == 0 {
		return
	}
	sort.Strings(offences)
	t.Fatalf("workspace mutation outside %s:\n  %s",
		doorFile, strings.Join(offences, "\n  "))
}

func nativeProductionFiles(t *testing.T) []string {
	t.Helper()
	var files []string
	err := filepath.WalkDir(".", func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return walkErr
		}
		if strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go") {
			files = append(files, filepath.Clean(path))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk native package tree: %v", err)
	}
	sort.Strings(files)
	return files
}
