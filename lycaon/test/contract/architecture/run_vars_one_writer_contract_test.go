package contract

import (
	"fmt"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// Vars writes load the run after locking so their CAS revision belongs to the critical section.
var runVarsWriterExemptions = map[string]string{
	// Document approval commits vars and the approval record together.
	"lycaon/internal/workflow/approval_service.go": "CommitBlueprintApproval commits vars and the approval record together",
}

// runVarsWriterHome is the primitive's own file.
const runVarsWriterHome = "lycaon/internal/workflow/runstate/variables.go"

// runVarsCallerHoldsLock participates in a wider caller-managed critical section.
const runVarsCallerHoldsLock = "stampLocked"

func TestRunVarsWritesGoThroughStampRunVars(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	var offences []string
	unused := map[string]bool{}
	for file := range runVarsWriterExemptions {
		unused[file] = true
	}
	corpus, err := contractcheck.LoadGoASTCorpus(filepath.Join(root, "lycaon", "internal"))
	contractcheck.FailErr(t, "load internal Go corpus", err)
	for _, source := range corpus.Files() {
		if source.IsTest {
			continue
		}
		rel, relErr := filepath.Rel(root, source.Path)
		contractcheck.FailErr(t, "relativize corpus path", relErr)
		rel = filepath.ToSlash(rel)
		writers := runVarsWritingFuncs(corpus.Fset, source.AST)
		if len(writers) == 0 {
			continue
		}
		if rel == runVarsWriterHome {
			offences = append(offences, runVarsFreshLoadOffences(rel, writers)...)
			continue
		}
		if _, listed := unused[rel]; listed {
			delete(unused, rel)
			offences = append(offences, runVarsFreshLoadOffences(rel, writers)...)
			continue
		}
		for _, fn := range writers {
			offences = append(offences, fmt.Sprintf("%s:%d: %s writes vars outside %s",
				rel, corpus.Fset.Position(fn.decl.Pos()).Line, fn.name, runVarsWriterHome))
		}
	}

	if len(offences) > 0 {
		sort.Strings(offences)
		t.Fatalf("scaffold vars write does not hold the read-modify-write shape "+
			"(load the run under the lock, immediately before the commit):\n  %s",
			strings.Join(offences, "\n  "))
	}
	if len(unused) > 0 {
		stale := make([]string, 0, len(unused))
		for file := range unused {
			stale = append(stale, file)
		}
		sort.Strings(stale)
		t.Fatalf("runVarsWriterExemptions lists files that no longer write vars directly "+
			"(delete the line, or the file moved):\n  %s", strings.Join(stale, "\n  "))
	}
}

// Writer primitives accept mutations, not caller-loaded runs.
func TestRunVarsPrimitiveTakesNoCallerSuppliedRun(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	path := filepath.Join(root, filepath.FromSlash(runVarsWriterHome))
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	contractcheck.FailErr(t, "parse run_vars.go", err)

	var offences []string
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Type.Params == nil {
			continue
		}
		for _, param := range fn.Type.Params.List {
			star, ok := param.Type.(*ast.StarExpr)
			if !ok {
				continue
			}
			sel, ok := star.X.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "WorkflowRun" {
				continue
			}
			offences = append(offences, fmt.Sprintf("%s:%d: %s accepts a caller-supplied run",
				runVarsWriterHome, fset.Position(param.Pos()).Line, fn.Name.Name))
		}
	}
	if len(offences) > 0 {
		sort.Strings(offences)
		t.Fatalf("a vars writer that accepts a run can commit against a stale revision; "+
			"pass a RunVarsMutation instead:\n  %s", strings.Join(offences, "\n  "))
	}
}

type runVarsWriter struct {
	name      string
	decl      *ast.FuncDecl
	writePos  token.Pos
	loadPos   token.Pos
	lockPos   token.Pos
	hasLoad   bool
	hasLock   bool
	readsVars bool
}

// runVarsWritingFuncs returns each function that commits the vars row, with the
// positions of its lock, its run load, and its write.
func runVarsWritingFuncs(fset *token.FileSet, file *ast.File) []runVarsWriter {
	_ = fset
	var out []runVarsWriter
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		w := runVarsWriter{name: fn.Name.Name, decl: fn}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			switch sel.Sel.Name {
			case "UpdateVars", "CommitBlueprintApproval":
				if !w.writePos.IsValid() {
					w.writePos = sel.Pos()
				}
			case "loadRun", "ActiveBySession", "Get":
				if !w.hasLoad {
					w.hasLoad, w.loadPos = true, sel.Pos()
				}
			case "Lock":
				if !w.hasLock {
					w.hasLock, w.lockPos = true, sel.Pos()
				}
			case "GetScaffoldVars":
				w.readsVars = true
			}
			return true
		})
		if w.writePos.IsValid() {
			out = append(out, w)
		}
	}
	return out
}

// runVarsFreshLoadOffences reports a writer whose CAS could carry a revision
// read before its critical section began.
func runVarsFreshLoadOffences(rel string, writers []runVarsWriter) []string {
	var out []string
	for _, w := range writers {
		if !w.hasLoad {
			out = append(out, fmt.Sprintf("%s: %s commits vars without loading the run in the same function",
				rel, w.name))
			continue
		}
		if w.loadPos > w.writePos {
			out = append(out, fmt.Sprintf("%s: %s loads the run after committing vars", rel, w.name))
			continue
		}
		if w.name == runVarsCallerHoldsLock {
			// The caller holds the lock; this function loads the run.
			continue
		}
		if !w.hasLock {
			out = append(out, fmt.Sprintf("%s: %s commits vars without lockRunVars", rel, w.name))
			continue
		}
		if w.lockPos > w.loadPos {
			out = append(out, fmt.Sprintf("%s: %s loads the run before taking lockRunVars — "+
				"the compare-and-set would carry a pre-lock revision", rel, w.name))
		}
	}
	return out
}
