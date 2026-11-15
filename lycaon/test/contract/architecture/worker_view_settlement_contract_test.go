package contract

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

const platformAdapterImportPrefix = "github.com/lycaon/lycaon/internal/layeredfs/adapter/"

// settlerArgument names each production deletion entrypoint and the position of its settler.
var settlerArgument = map[string]int{
	"ReconcileStaleSandboxes":       3,
	"ReconcileProjectSandboxes":     3,
	"ReconcileSandboxRoots":         4,
	"ReconcileSelectedSandboxRoots": 6,
}

// TestWorkerDataDeletionSettlesAndPlatformAdaptersStayBehindTheContract holds the two properties
// a wiring gap would lose silently: no production deletion path may opt out of settlement, and
// only the composition package names a platform adapter. Retention must take the one evictor.
func TestWorkerDataDeletionSettlesAndPlatformAdaptersStayBehindTheContract(t *testing.T) {
	t.Parallel()
	root := filepath.Join(contractcheck.RepoRoot(t), "lycaon", "internal")
	var offences []string
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly|parser.ParseComments)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		for _, spec := range file.Imports {
			imported, _ := strconv.Unquote(spec.Path.Value)
			isPlatform := imported == platformAdapterImportPrefix+"fuselinux" || imported == platformAdapterImportPrefix+"nfsdarwin"
			if isPlatform && !strings.HasPrefix(rel, "layeredfs/adapter/") {
				offences = append(offences, rel+": imports "+imported)
			}
		}
		if !strings.HasPrefix(rel, "workspace/") && !strings.HasPrefix(rel, "worker/") && !strings.HasPrefix(rel, "app/") && !strings.HasPrefix(rel, "api/") {
			return nil
		}
		full, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(full, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			name := calledName(call)
			if position, governed := settlerArgument[name]; governed && len(call.Args) > position {
				if literal, isIdent := call.Args[position].(*ast.Ident); isIdent && literal.Name == "nil" {
					offences = append(offences, rel+": "+name+" passes a nil settler")
				}
			}
			return true
		})
		if strings.HasPrefix(rel, "app/") || strings.HasPrefix(rel, "api/") {
			ast.Inspect(full, func(node ast.Node) bool {
				if sel, ok := node.(*ast.SelectorExpr); ok && sel.Sel.Name == "EvictJobTree" {
					offences = append(offences, rel+": retention bypasses worker.BranchEvictor")
				}
				return true
			})
		}
		return nil
	})
	contractcheck.FailErr(t, "scan production sources", err)
	if len(offences) > 0 {
		t.Fatalf("worker view settlement contract:\n  %s", strings.Join(offences, "\n  "))
	}
}

func calledName(call *ast.CallExpr) string {
	switch fn := call.Fun.(type) {
	case *ast.SelectorExpr:
		return fn.Sel.Name
	case *ast.Ident:
		return fn.Name
	}
	return ""
}
