package forbiddencalls

import (
	"go/ast"
	"go/types"

	"github.com/lycaon/lycaon/test/contract/invariants"
)

const (
	commandEvaluatorDir = "internal/commandprogram"
	commandDispatchDir  = "internal/tools/native/command"
	invocationDir       = "internal/invocation"
)

var commandSettlement = []invariants.Invariant{{
	Name: "command-redirects-publish-through-settlement",
	Anchors: []invariants.Anchor{
		invariants.Decl(commandEvaluatorDir, "StagedOutput.Publish"),
		invariants.Func(commandEvaluatorDir, "stagedRedirect.finish"),
	},
	Check: func(r *invariants.Run) {
		requireCommandMethodBoundary(r, commandEvaluatorDir, "StagedOutput", "Publish", commandEvaluatorDir, "stagedRedirect.finish")
	},
}, {
	Name: "command-child-receipts-settle-through-dispatcher",
	Anchors: []invariants.Anchor{
		invariants.Func(invocationDir, "SQLRecorder.SettleChild"),
		invariants.Func(commandDispatchDir, "ProgramDispatcher.settleWith"),
	},
	Check: func(r *invariants.Run) {
		requireCommandMethodBoundary(r, invocationDir, "SQLRecorder", "SettleChild", commandDispatchDir, "ProgramDispatcher.settleWith")
	},
}}

func requireCommandMethodBoundary(r *invariants.Run, declaringDir, typeName, methodName, scopeDir, boundary string) {
	declaring := r.TypedPackage(declaringDir)
	receiver := declaring.Types.Scope().Lookup(typeName).Type()
	method, _, _ := types.LookupFieldOrMethod(receiver, true, nil, methodName)
	refs := 0
	for _, pkg := range r.TypedPackages(scopeDir) {
		for _, f := range pkg.Files {
			ast.Inspect(f.AST, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != methodName {
					return true
				}
				selection := pkg.Info.Selections[sel]
				// The typed signature also catches method values and narrowed interfaces.
				if selection == nil || selection.Kind() == types.FieldVal || !types.Identical(selection.Obj().Type(), method.Type()) {
					return true
				}
				if fn := f.EnclosingFunc(sel.Pos()); f.Dir == scopeDir && fn != nil && invariants.FuncName(fn) == boundary {
					refs++
				} else {
					r.ReportAt(sel.Pos(), "%s.%s must be reached through %s", typeName, methodName, boundary)
				}
				return true
			})
		}
	}
	r.Expect(refs, "typed "+typeName+"."+methodName+" references inside "+boundary)
}
