package contract

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// TestWorkerPromotePathOutcomeCallsitesExhaustive requires a production
// reference in the worker package for every WorkerPromotePathOutcome constant.
func TestWorkerPromotePathOutcomeCallsitesExhaustive(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	declared := workerPromoteOutcomeConsts(t, root)
	if len(declared) == 0 {
		t.Fatalf("no WorkerPromotePathOutcome consts found in pkg/api/worker_types.go")
	}

	workerDir := filepath.Join(root, "lycaon", "internal", "worker")
	_, files := contractcheck.ParseNonTestGoTree(t, workerDir)

	used := map[string]bool{}
	for _, file := range files {
		ast.Inspect(file, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			ident, ok := sel.X.(*ast.Ident)
			if !ok || ident.Name != "api" {
				return true
			}
			if _, ok := declared[sel.Sel.Name]; ok {
				used[sel.Sel.Name] = true
			}
			return true
		})
	}

	var missing []string
	for name := range declared {
		if !used[name] {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("WorkerPromotePathOutcome consts declared in pkg/api but never referenced in lycaon/internal/worker: %v\n"+
			"Either emit the value from a production code path (typically buildPathStatus) or remove it from the enum.", missing)
	}
}

// TestWorkerPromoteReasonCodesReferencedInProduction does the same for the
// WorkerPromoteReason* string constants in worker and session code.
func TestWorkerPromoteReasonCodesReferencedInProduction(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	declared := workerPromoteReasonConsts(t, root)
	if len(declared) == 0 {
		t.Skip("no WorkerPromoteReason* consts in pkg/api/worker_types.go")
	}

	workerDir := filepath.Join(root, "lycaon", "internal", "worker")
	sessionDir := filepath.Join(root, "lycaon", "internal", "session")
	_, files1 := contractcheck.ParseNonTestGoTree(t, workerDir)
	_, files2 := contractcheck.ParseNonTestGoTree(t, sessionDir)
	files := make([]*ast.File, 0, len(files1)+len(files2))
	files = append(files, files1...)
	files = append(files, files2...)

	used := map[string]bool{}
	for _, file := range files {
		ast.Inspect(file, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			ident, ok := sel.X.(*ast.Ident)
			if !ok || ident.Name != "api" {
				return true
			}
			if _, ok := declared[sel.Sel.Name]; ok {
				used[sel.Sel.Name] = true
			}
			return true
		})
	}

	var missing []string
	for name := range declared {
		if !used[name] {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("WorkerPromoteReason* consts declared in pkg/api but never referenced in worker/session production code: %v", missing)
	}
}

func workerPromoteOutcomeConsts(t *testing.T, root string) map[string]struct{} {
	t.Helper()
	fset := token.NewFileSet()
	path := filepath.Join(root, "lycaon", "pkg", "api", "worker_types.go")
	file, err := parser.ParseFile(fset, path, nil, 0)
	contractcheck.FailErr(t, "parse worker_types.go", err)
	out := map[string]struct{}{}
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
			ident, ok := vs.Type.(*ast.Ident)
			if !ok || ident.Name != "WorkerPromotePathOutcome" {
				continue
			}
			for _, name := range vs.Names {
				out[name.Name] = struct{}{}
			}
		}
	}
	return out
}

// workerPromoteReasonConsts returns the untyped consts named WorkerPromoteReason*.
func workerPromoteReasonConsts(t *testing.T, root string) map[string]struct{} {
	t.Helper()
	fset := token.NewFileSet()
	path := filepath.Join(root, "lycaon", "pkg", "api", "worker_types.go")
	file, err := parser.ParseFile(fset, path, nil, 0)
	contractcheck.FailErr(t, "parse worker_types.go", err)
	out := map[string]struct{}{}
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
			for _, name := range vs.Names {
				if len(name.Name) > len("WorkerPromoteReason") &&
					name.Name[:len("WorkerPromoteReason")] == "WorkerPromoteReason" {
					out[name.Name] = struct{}{}
				}
			}
		}
	}
	return out
}
