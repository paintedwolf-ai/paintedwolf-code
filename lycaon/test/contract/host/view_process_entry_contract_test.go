package contract

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// rawProcessDirExemptions lists production code that sets a working directory on an os/exec command.
// A worker view is served inside the engine, so a child entering one before exec can hang the engine
// under GC. Everything else starts through internal/exec, whose helper enters the directory after exec.
var rawProcessDirExemptions = map[string]string{
	"lycaon/internal/exec/pipeline.go":               "records the requested directory on a scratch command that is never started; the launch pins it for the helper",
	"lycaon/internal/confine/process_pin_binding.go": "clears Dir on the helper command, which is what makes entry happen after exec",
}

// TestWorkerViewProcessesEnterThroughTheLaunchHelper stops a new direct working-directory start:
// any assignment to a command's Dir field outside the exemptions fails, whatever the directory is,
// because nothing at the call site can prove a directory is not on a view.
func TestWorkerViewProcessesEnterThroughTheLaunchHelper(t *testing.T) {
	repo := contractcheck.RepoRoot(t)
	var offenders []string
	seen := map[string]bool{}
	visit := func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			if d.Name() == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, err := filepath.Rel(repo, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(file, func(n ast.Node) bool {
			if !setsCommandDir(n) {
				return true
			}
			seen[rel] = true
			if _, exempt := rawProcessDirExemptions[rel]; !exempt {
				offenders = append(offenders, fset.Position(n.Pos()).String())
			}
			return true
		})
		return nil
	}
	// Other binaries serve no view, so a child of theirs parks nothing.
	contractcheck.FailErr(t, "walk engine sources", filepath.WalkDir(filepath.Join(repo, "lycaon", "internal"), visit))
	sort.Strings(offenders)
	if len(offenders) > 0 {
		t.Fatalf("processes must start through internal/exec with ExecOpts.Dir, not a raw working directory:\n%s", strings.Join(offenders, "\n"))
	}
	for rel := range rawProcessDirExemptions {
		if !seen[rel] {
			t.Errorf("stale exemption %s: it no longer sets a command's working directory", rel)
		}
	}
}

// setsCommandDir recognizes `<cmd>.Dir = ...`, including tuple assignments, and `exec.Cmd{Dir: ...}`.
func setsCommandDir(n ast.Node) bool {
	switch node := n.(type) {
	case *ast.AssignStmt:
		for _, lhs := range node.Lhs {
			if sel, ok := lhs.(*ast.SelectorExpr); ok && sel.Sel.Name == "Dir" && commandLikeName(sel.X) {
				return true
			}
		}
	case *ast.CompositeLit:
		sel, ok := node.Type.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Cmd" {
			return false
		}
		for _, elt := range node.Elts {
			if kv, ok := elt.(*ast.KeyValueExpr); ok {
				if key, ok := kv.Key.(*ast.Ident); ok && key.Name == "Dir" {
					return true
				}
			}
		}
	}
	return false
}

// commandLikeName limits the check to os/exec commands by the receiver's name, which is the only
// fact a syntax scan has; other types with a Dir field use other names in this tree.
func commandLikeName(x ast.Expr) bool {
	id, ok := x.(*ast.Ident)
	if !ok {
		return false
	}
	name := strings.ToLower(id.Name)
	return name == "c" || strings.Contains(name, "cmd") || strings.Contains(name, "command") || strings.Contains(name, "proc") || name == "final"
}

// viewPathConsumers are the packages handed worker view paths. The server runs in this process, so a
// flush or mapping there can stop the whole engine: darwin's File.Sync holds its scheduler slot across
// a call the server must answer, and a page fault on a mapped view file does the same or kills the
// process when the page-in fails. Their flushes go through internal/fssync and they map nothing.
var viewPathConsumers = []string{
	"lycaon/internal/workspace", "lycaon/internal/worker", "lycaon/internal/session", "lycaon/internal/tools",
	"lycaon/internal/toolhost", "lycaon/internal/layeredfs/consumer", "lycaon/internal/api/sourceapi",
}

func TestViewPathConsumersFlushThroughFssyncAndMapNothing(t *testing.T) {
	repo := contractcheck.RepoRoot(t)
	var offenders []string
	for _, pkg := range viewPathConsumers {
		err := filepath.WalkDir(filepath.Join(repo, filepath.FromSlash(pkg)), func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(repo, path)
			if err != nil {
				return err
			}
			ast.Inspect(parsed, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				selector, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				if name := selector.Sel.Name; (name == "Sync" && len(call.Args) == 0) || name == "Mmap" {
					offenders = append(offenders, filepath.ToSlash(rel)+": "+name)
				}
				return true
			})
			return nil
		})
		contractcheck.FailErr(t, "walk "+pkg, err)
	}
	sort.Strings(offenders)
	if len(offenders) > 0 {
		t.Fatalf("view path consumers must flush through internal/fssync and never map:\n%s", strings.Join(offenders, "\n"))
	}
}
