package contract

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// TestLoopEngineDepsReadOnlyViaAccessor: LoopEngine.deps is read only through
// loopDeps(), which snapshots under depsMu. SetDeps writes it during a prompt turn
// while host-wake paths read it concurrently.
func TestLoopEngineDepsReadOnlyViaAccessor(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	dir := filepath.Join(root, "lycaon", "internal", "coordinator", "loopwake")
	violations := scanLoopEngineDepsReads(t, dir)
	if len(violations) > 0 {
		contractcheck.FailViolations(t, "loopwake methods must read deps via l.loopDeps() (depsMu), never the field directly", violations)
	}
}

// loopEngineDepsAccessors are the only methods permitted to touch the raw field.
var loopEngineDepsAccessors = map[string]struct{}{"loopDeps": {}, "SetDeps": {}}

// scanLoopEngineDepsReads parses every non-test .go in dir and returns a
// violation for each `<recv>.deps` selector inside a *LoopEngine method other
// than the allowed accessors.
func scanLoopEngineDepsReads(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	testutil.FailErr(t, "read loopwake dir", err)
	fset := token.NewFileSet()
	var violations []string
	for _, ent := range entries {
		name := ent.Name()
		if ent.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, perr := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.SkipObjectResolution)
		testutil.FailErr(t, "parse "+name, perr)
		violations = append(violations, loopEngineDepsViolations(fset, file)...)
	}
	return violations
}

func loopEngineDepsViolations(fset *token.FileSet, file *ast.File) []string {
	var out []string
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		recv := loopEngineReceiverName(fn)
		if recv == "" {
			continue
		}
		if _, allowed := loopEngineDepsAccessors[fn.Name.Name]; allowed {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "deps" {
				return true
			}
			id, ok := sel.X.(*ast.Ident)
			if !ok || id.Name != recv {
				return true
			}
			p := fset.Position(sel.Pos())
			out = append(out, filepath.Base(p.Filename)+":"+strconv.Itoa(p.Line)+": "+fn.Name.Name+
				" reads "+recv+".deps directly — snapshot via "+recv+".loopDeps() (RLock) instead")
			return true
		})
	}
	return out
}

// loopEngineReceiverName returns the receiver identifier for a method on
// *LoopEngine, or "" for any other declaration.
func loopEngineReceiverName(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return ""
	}
	field := fn.Recv.List[0]
	star, ok := field.Type.(*ast.StarExpr)
	if !ok {
		return ""
	}
	id, ok := star.X.(*ast.Ident)
	if !ok || id.Name != "LoopEngine" || len(field.Names) == 0 {
		return ""
	}
	return field.Names[0].Name
}

func relRepoPath(root, path string) string {
	if rel, err := filepath.Rel(root, path); err == nil {
		return filepath.ToSlash(rel)
	}
	return filepath.ToSlash(path)
}

// Synthetic controls keep the concurrency AST guard live and precise.
func TestConcurrencyDisciplineRulesFireOnSyntheticViolations(t *testing.T) {
	t.Parallel()
	const src = `package loopwake
type LoopEngine struct{ deps int }
func (l *LoopEngine) loopDeps() int { return l.deps }            // OK — accessor
func (l *LoopEngine) Good() int     { return l.loopDeps() }       // OK — via accessor
func (l *LoopEngine) Bad() int      { return l.deps }             // VIOLATION — direct field
`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "synthetic.go", src, parser.SkipObjectResolution)
	testutil.FailErr(t, "parse synthetic", err)

	// Exactly the Bad() direct field read should fire — not loopDeps() or Good().
	depsViolations := loopEngineDepsViolations(fset, file)
	if len(depsViolations) != 1 || !strings.Contains(depsViolations[0], "Bad reads l.deps directly") {
		t.Fatalf("deps guard: want one violation for Bad(), got %v", depsViolations)
	}
}
