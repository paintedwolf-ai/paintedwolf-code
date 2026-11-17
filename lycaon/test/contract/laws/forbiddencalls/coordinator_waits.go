package forbiddencalls

import (
	"go/ast"
	"go/types"

	"github.com/lycaon/lycaon/test/contract/laws"
)

// sleepMovers are the arguments that may name who ends a sleep. A defaulted
// SleepMover reads as "not the host" and would publish no lease.
var sleepMovers = []string{
	"SleepMoverHost", "SleepMoverUser", "loopwake.SleepMoverHost", "loopwake.SleepMoverUser",
	// A re-arm carries forward the mover of the wait it continues.
	"l.activeSleepMover",
}

var coordinatorWaits = []laws.Rule{{
	// Every armed sleep names who ends it.
	Name:    "enter-sleep-names-its-mover",
	Anchors: []laws.Anchor{laws.Func("internal/coordinator/loopwake", "Waits.EnterSleep"), laws.Decl("internal/coordinator/loopwake", "SleepMoverHost")},
	Check: func(r *laws.Run) {
		calls := 0
		for _, f := range r.GoFiles(laws.Production, "internal", "cmd", "pkg", "config", "test") {
			f.Calls(f.AST, func(call *ast.CallExpr, _, _ string) {
				if laws.CalleeName(call) != "EnterSleep" {
					return
				}
				calls++
				if len(call.Args) != 7 {
					r.ReportAt(call.Pos(), "EnterSleep takes a mover; this call passes %d arguments", len(call.Args))
					return
				}
				mover := call.Args[6]
				if inner, ok := mover.(*ast.CallExpr); ok {
					mover = inner.Fun
				}
				if text := exprString(mover); !contains(sleepMovers, text) {
					r.ReportAt(call.Pos(), "mover %s is not a SleepMover constant", text)
				}
			})
		}
		r.Expect(calls, "EnterSleep calls")
	},
}, {
	// The sleep deadline moves only where the lease moves with it.
	Name:    "sleep-deadline-writes-stay-in-the-lease-funnel",
	Anchors: []laws.Anchor{laws.Package("internal/coordinator/loopwake")},
	Exempt: []laws.Exemption{
		{Key: "enterSleep", Reason: "arms the sleep and opens its lease"},
		{Key: "breakSleep", Reason: "ends the sleep and closes its lease"},
		{Key: "DisarmTimerBackstop", Reason: "disarms the backstop and moves the lease with it"},
	},
	Check: func(r *laws.Run) {
		pkg := r.TypedPackage("internal/coordinator/loopwake")
		for _, f := range pkg.Files {
			f.Funcs(func(fn *ast.FuncDecl) {
				ast.Inspect(fn.Body, func(n ast.Node) bool {
					assign, ok := n.(*ast.AssignStmt)
					if !ok {
						return true
					}
					for _, lhs := range assign.Lhs {
						if sel, ok := lhs.(*ast.SelectorExpr); ok && sel.Sel.Name == "until" && isSleepState(pkg.Info.Selections[sel]) &&
							!r.Exempt(fn.Name.Name) {
							r.ReportAt(assign.Pos(), "%s writes the sleep deadline outside the lease funnel", fn.Name.Name)
						}
					}
					return true
				})
			})
		}
	},
}}

// isSleepState reports a field selection on the per-session sleep state,
// the struct that holds both until and armed.
func isSleepState(sel *types.Selection) bool {
	if sel == nil || sel.Kind() != types.FieldVal {
		return false
	}
	recv := sel.Recv()
	if ptr, ok := recv.(*types.Pointer); ok {
		recv = ptr.Elem()
	}
	st, ok := recv.Underlying().(*types.Struct)
	if !ok {
		return false
	}
	for i := range st.NumFields() {
		if st.Field(i).Name() == "armed" {
			return true
		}
	}
	return false
}
