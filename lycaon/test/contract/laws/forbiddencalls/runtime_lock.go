package forbiddencalls

import (
	"go/ast"
	"go/token"

	"github.com/lycaon/lycaon/test/contract/laws"
)

// runtimeEngineFields hold engines whose methods re-enter Runtime through
// session-manager callbacks.
var runtimeEngineFields = []string{"assembler", "kicks", "board", "loop"}

var runtimeLock = []laws.Rule{{
	// Runtime methods snapshot an engine field under depsMu and call it after
	// Unlock: an engine call re-enters Runtime and would deadlock.
	Name:    "runtime-methods-release-deps-mu-before-engine-calls",
	Anchors: runtimeLockAnchors(),
	Check: func(r *laws.Run) {
		for _, f := range r.GoFiles(func(f *laws.GoFile) bool { return !f.IsTest && f.Dir == "internal/coordinator" }, "internal/coordinator") {
			f.Funcs(func(fn *ast.FuncDecl) {
				if fn.Recv == nil {
					return
				}
				if star, ok := fn.Recv.List[0].Type.(*ast.StarExpr); !ok || laws.TypeName(star) != "Runtime" {
					return
				}
				for _, pos := range lockedEngineCalls(fn) {
					r.ReportAt(pos, "%s calls an engine while r.depsMu is held; snapshot, Unlock, then call", fn.Name.Name)
				}
			})
		}
	},
}}

func runtimeLockAnchors() []laws.Anchor {
	out := []laws.Anchor{laws.Decl("internal/coordinator", "Runtime.depsMu")}
	for _, field := range runtimeEngineFields {
		out = append(out, laws.Decl("internal/coordinator", "Runtime."+field))
	}
	return out
}

// lockedEngineCalls returns r.<engine>.Method(...) calls made between
// r.depsMu.Lock() and the next Unlock, or anywhere after a deferred Unlock.
func lockedEngineCalls(fn *ast.FuncDecl) []token.Pos {
	var out []token.Pos
	locked, deferUnlock := false, false
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.CallExpr:
			sel, ok := node.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			switch exprString(sel) {
			case "r.depsMu.Lock":
				locked = true
				return true
			case "r.depsMu.Unlock":
				locked = locked && deferUnlock
				return true
			}
			if inner, ok := sel.X.(*ast.SelectorExpr); ok && locked && exprString(inner.X) == "r" && contains(runtimeEngineFields, inner.Sel.Name) {
				out = append(out, node.Pos())
			}
		case *ast.DeferStmt:
			deferUnlock = deferUnlock || exprString(node.Call.Fun) == "r.depsMu.Unlock"
		}
		return true
	})
	return out
}
