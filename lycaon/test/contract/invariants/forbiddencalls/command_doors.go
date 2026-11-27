package forbiddencalls

import (
	"go/ast"
	"slices"

	"github.com/lycaon/lycaon/test/contract/invariants"
)

var commandExternalStartRefs = map[string][]string{
	"github.com/lycaon/lycaon/internal/exec": {
		"StartPipelineAsync", "RunPipeline", "PrepareCommand",
		"StartPrepared", "StartPreparedWith", "StartPTY",
		"StartInOwnGroup", "StartDescriptorHelper",
	},
	"os/exec": {"Command", "CommandContext"},
}

var commandReadCreditMethods = []string{"RememberAgentRead", "RememberAgentReadSpans"}

func enclosingReceiverMethod(fn *ast.FuncDecl) string {
	if fn == nil {
		return ""
	}
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return fn.Name.Name
	}
	expr := fn.Recv.List[0].Type
	if star, ok := expr.(*ast.StarExpr); ok {
		expr = star.X
	}
	if ident, ok := expr.(*ast.Ident); ok {
		return ident.Name + "." + fn.Name.Name
	}
	return fn.Name.Name
}

var commandDoors = []invariants.Invariant{{
	// BB10 I11: external process starts in internal/tools/native/command route
	// exclusively through ExternalStages.Launch so launch-input verification,
	// confinement, and policy gates cannot be bypassed by a second dispatch path.
	Name: "command-external-starts-through-launch-door",
	Anchors: []invariants.Anchor{
		invariants.Decl("internal/tools/native/command", "ExternalStages.Launch"),
		invariants.Func("internal/exec", "StartPipelineAsync"),
	},
	Check: func(r *invariants.Run) {
		for _, f := range r.GoFiles(invariants.Production, "internal/tools/native/command") {
			for pkg := range commandExternalStartRefs {
				if f.DotImports(pkg) {
					r.Report("%s dot-imports %s: external process starts must route through ExternalStages.Launch", f.Rel, pkg)
				}
			}
			f.Selectors(f.AST, func(sel *ast.SelectorExpr, pkg, name string) {
				if !slices.Contains(commandExternalStartRefs[pkg], name) {
					return
				}
				if enclosingReceiverMethod(f.EnclosingFunc(sel.Pos())) == "ExternalStages.Launch" {
					return
				}
				r.Report("%s uses %s outside ExternalStages.Launch: external process starts must route through ExternalStages.Launch",
					f.Site(sel.Pos()), invariants.SelectorName(sel))
			})
		}
	},
}, {
	// BB10 I13: Portable Command evaluator and runtime packages never credit
	// editor read bases directly; command output delivery credits read spans
	// only through tools.RememberCommandSourceDelivery.
	Name: "command-read-credit-through-delivery-bridge",
	Anchors: []invariants.Anchor{
		invariants.Func("internal/tools", "RememberCommandSourceDelivery"),
		invariants.Decl("internal/tools", "EditorDocuments"),
	},
	Check: func(r *invariants.Run) {
		for _, f := range r.GoFiles(invariants.Production, "internal/commandprogram", "internal/tools/native/command") {
			ast.Inspect(f.AST, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if !ok || !slices.Contains(commandReadCreditMethods, sel.Sel.Name) {
					return true
				}
				r.Report("%s calls %s directly: Portable Command read credit must route through tools.RememberCommandSourceDelivery",
					f.Site(sel.Pos()), sel.Sel.Name)
				return true
			})
		}
	},
}}
