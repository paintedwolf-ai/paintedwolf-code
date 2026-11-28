package forbiddencalls

import (
	"go/ast"
	"go/types"
	"slices"

	"github.com/lycaon/lycaon/test/contract/invariants"
)

const toolsPkg = "github.com/lycaon/lycaon/internal/tools"

var commandExternalStartRefs = map[string][]string{
	"github.com/lycaon/lycaon/internal/exec": {
		"StartPipelineAsync", "RunPipeline", "PrepareCommand",
		"StartPrepared", "StartPreparedWith", "StartPTY",
		"StartInOwnGroup", "StartDescriptorHelper",
	},
	"os/exec": {"Command", "CommandContext"},
}

var commandReadCreditMethods = map[string]bool{
	"(" + toolsPkg + ".EditorDocuments).RememberAgentRead":      true,
	"(" + toolsPkg + ".EditorDocuments).RememberAgentReadSpans": true,
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
				if fn := f.EnclosingFunc(sel.Pos()); fn != nil && invariants.FuncName(fn) == "ExternalStages.Launch" {
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
		for _, pkg := range r.TypedPackages("internal/commandprogram", "internal/tools/native/command") {
			for _, f := range pkg.Files {
				ast.Inspect(f.AST, func(n ast.Node) bool {
					sel, ok := n.(*ast.SelectorExpr)
					if !ok {
						return true
					}
					ts := pkg.Info.Selections[sel]
					if ts == nil {
						return true
					}
					fn, ok := ts.Obj().(*types.Func)
					if !ok || !commandReadCreditMethods[fn.FullName()] {
						return true
					}
					r.Report("%s calls %s directly: Portable Command read credit must route through tools.RememberCommandSourceDelivery",
						f.Site(sel.Pos()), fn.FullName())
					return true
				})
			}
		}
	},
}}
