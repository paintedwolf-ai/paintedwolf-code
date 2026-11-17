package forbiddencalls

import (
	"go/ast"
	"go/token"
	"go/types"

	"github.com/lycaon/lycaon/test/contract/laws"
)

const anchorPkg = "github.com/lycaon/lycaon/internal/coordinator/anchor"

var coordinator = []laws.Rule{{
	// A match context carries a workflow only through anchor.RunMatch, which
	// binds the workflow version.
	Name:    "match-context-workflow-through-run-match",
	Anchors: []laws.Anchor{laws.Decl("internal/coordinator/anchor", "MatchContext.Workflow"), laws.Func("internal/coordinator/anchor", "RunMatch")},
	Exempt:  []laws.Exemption{{Key: "internal/coordinator/anchor/binding.go:RunMatch", Reason: "the binding constructor itself"}},
	Check: func(r *laws.Run) {
		for _, f := range r.GoFiles(laws.Production, "internal") {
			ast.Inspect(f.AST, func(n ast.Node) bool {
				lit, ok := n.(*ast.CompositeLit)
				if !ok || !isMatchContext(f, lit.Type) || !setsField(lit, "Workflow") || r.Exempt(f.Site(lit.Pos())) {
					return true
				}
				r.ReportAt(lit.Pos(), "builds MatchContext{Workflow: ...}; use anchor.RunMatch to bind the workflow version")
				return true
			})
		}
	},
}}

func isMatchContext(f *laws.GoFile, typ ast.Expr) bool {
	if id, ok := typ.(*ast.Ident); ok {
		return id.Name == "MatchContext" && f.Pkg == anchorPkg
	}
	pkg, name, ok := f.Qualified(typ)
	return ok && pkg == anchorPkg && name == "MatchContext"
}

// setsField reports a keyed composite literal element for field.
func setsField(lit *ast.CompositeLit, field string) bool {
	for _, elt := range lit.Elts {
		if kv, ok := elt.(*ast.KeyValueExpr); ok {
			if key, ok := kv.Key.(*ast.Ident); ok && key.Name == field {
				return true
			}
		}
	}
	return false
}

var transcriptPublish = []laws.Rule{{
	// AppendMessages stamps ord and seq onto its variadic slice; publishing
	// the pre-stamp value would pin the card at the top of the transcript.
	Name:    "publish-message-append-uses-the-stamped-copy",
	Anchors: []laws.Anchor{laws.Func("internal/events", "Publisher.PublishMessageAppend"), laws.Func("internal/session/store", "sqlTranscript.AppendMessages")},
	Check: func(r *laws.Run) {
		for _, f := range r.GoFiles(laws.Production, "internal") {
			f.Funcs(func(fn *ast.FuncDecl) {
				copied := map[string]bool{}
				f.Calls(fn.Body, func(call *ast.CallExpr, _, _ string) {
					switch laws.CalleeName(call) {
					case "AppendMessages":
						if len(call.Args) >= 3 && !call.Ellipsis.IsValid() {
							if id, ok := call.Args[len(call.Args)-1].(*ast.Ident); ok {
								copied[id.Name] = true
							}
						}
					case "PublishMessageAppend":
						if len(call.Args) < 4 {
							return
						}
						if id, ok := call.Args[len(call.Args)-1].(*ast.Ident); ok && copied[id.Name] {
							r.ReportAt(call.Pos(), "publishes %s after appending it by value; publish the stamped slice element", id.Name)
						}
					}
				})
			})
		}
	},
}}

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
