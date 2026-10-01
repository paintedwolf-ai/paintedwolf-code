package contract

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// *Runtime methods release depsMu before calling methods on engine fields;
// they snapshot the field under the lock and call through the snapshot.
// Engine calls re-enter Runtime through session-manager callbacks
// (PhaseEnterHook → Manager.NudgeCoordinatorLoop → Runtime.CoordinatorLoop),
// which would deadlock on the same goroutine.
func TestRuntimeMethodsDoNotHoldDepsMuOverEngineCalls(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	path := filepath.Join(root, "lycaon", "internal", "coordinator", "runtime.go")
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	testutil.FailErr(t, "parse runtime.go", err)

	// Engine fields whose methods can re-enter Runtime via host callbacks.
	engineFields := map[string]struct{}{
		"assembly":     {},
		"autoContinue": {},
		"kicks":        {},
		"board":        {},
		"loop":         {},
	}

	var violations []string
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv == nil || len(fn.Recv.List) == 0 {
			continue
		}
		// Must be a method on *Runtime.
		star, ok := fn.Recv.List[0].Type.(*ast.StarExpr)
		if !ok {
			continue
		}
		ident, ok := star.X.(*ast.Ident)
		if !ok || ident.Name != "Runtime" {
			continue
		}
		if fn.Body == nil {
			continue
		}

		violations = append(violations, scanForLockedEngineCalls(fset, fn, engineFields)...)
	}

	if len(violations) > 0 {
		contractcheck.FailViolations(t, "Runtime methods must release depsMu before calling assembly/autoContinue/kicks/board/loop; engine calls re-enter Runtime and deadlock on the same goroutine", violations)
	}
}

// scanForLockedEngineCalls walks fn.Body, tracking whether depsMu is held.
// Returns positional violation strings for each engine method call made while
// the lock is held.
func scanForLockedEngineCalls(fset *token.FileSet, fn *ast.FuncDecl, engineFields map[string]struct{}) []string {
	var violations []string
	// The lock is held from Lock() to the next Unlock(); after a deferred
	// Unlock it is held for the rest of the body.
	locked := false
	deferUnlock := false

	ast.Inspect(fn.Body, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.CallExpr:
			if isMethodCall(node, "depsMu", "Lock") {
				locked = true
				return true
			}
			if isMethodCall(node, "depsMu", "Unlock") {
				if !deferUnlock {
					locked = false
				}
				return true
			}
			if !locked {
				return true
			}
			sel, ok := node.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			// Identify `r.<engine>.Method(...)` (selector on a selector).
			inner, ok := sel.X.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			recv, ok := inner.X.(*ast.Ident)
			if !ok || recv.Name != "r" {
				return true
			}
			if _, isEngine := engineFields[inner.Sel.Name]; !isEngine {
				return true
			}
			violations = append(violations, posLine(fset, node.Pos())+": "+fn.Name.Name+
				" calls r."+inner.Sel.Name+"."+sel.Sel.Name+"(...) while r.depsMu is held — snapshot first, then Unlock, then call")
		case *ast.DeferStmt:
			// `defer r.depsMu.Unlock()` extends the locked region to function end.
			if call, ok := node.Call.Fun.(*ast.SelectorExpr); ok {
				if inner, ok := call.X.(*ast.SelectorExpr); ok {
					if id, ok := inner.X.(*ast.Ident); ok && id.Name == "r" &&
						inner.Sel.Name == "depsMu" && call.Sel.Name == "Unlock" {
						deferUnlock = true
					}
				}
			}
		}
		return true
	})
	return violations
}

func isMethodCall(call *ast.CallExpr, fieldName, methodName string) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != methodName {
		return false
	}
	inner, ok := sel.X.(*ast.SelectorExpr)
	if !ok || inner.Sel.Name != fieldName {
		return false
	}
	id, ok := inner.X.(*ast.Ident)
	return ok && id.Name == "r"
}

func posLine(fset *token.FileSet, pos token.Pos) string {
	p := fset.Position(pos)
	return filepath.Base(p.Filename) + ":" + strconv.Itoa(p.Line)
}

// Verify the AST contract itself works: inject a synthetic violation pattern
// through string analysis to confirm the rule fires on the unsafe shape.
func TestLockDisciplineRuleFiresOnSyntheticViolation(t *testing.T) {
	t.Parallel()
	src := `package coordinator
import "sync"
type stub struct{}
func (s *stub) Call() {}
type Runtime struct {
	depsMu sync.Mutex
	assembly *stub
}
func (r *Runtime) Bad() {
	r.depsMu.Lock()
	defer r.depsMu.Unlock()
	r.assembly.Call() // FORBIDDEN
}
func (r *Runtime) Good() {
	r.depsMu.Lock()
	a := r.assembly
	r.depsMu.Unlock()
	a.Call() // OK — lock released
}
`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "synthetic.go", src, parser.SkipObjectResolution)
	testutil.FailErr(t, "parse synthetic", err)

	engineFields := map[string]struct{}{"assembly": {}}
	var allViolations []string
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv == nil || len(fn.Recv.List) == 0 {
			continue
		}
		star, ok := fn.Recv.List[0].Type.(*ast.StarExpr)
		if !ok {
			continue
		}
		if id, ok := star.X.(*ast.Ident); !ok || id.Name != "Runtime" {
			continue
		}
		if fn.Body == nil {
			continue
		}
		allViolations = append(allViolations, scanForLockedEngineCalls(fset, fn, engineFields)...)
	}
	var sawBad, sawGood bool
	for _, v := range allViolations {
		if strings.Contains(v, "Bad calls") {
			sawBad = true
		}
		if strings.Contains(v, "Good calls") {
			sawGood = true
		}
	}
	if !sawBad {
		t.Fatalf("AST rule failed to catch synthetic Bad method violation; violations = %v", allViolations)
	}
	if sawGood {
		t.Fatalf("AST rule false-positived on Good method (snapshot-then-Unlock pattern); violations = %v", allViolations)
	}
}
