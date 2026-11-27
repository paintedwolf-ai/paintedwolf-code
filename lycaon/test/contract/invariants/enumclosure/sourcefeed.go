package enumclosure

import (
	"go/ast"
	"go/types"
	"strings"

	"github.com/lycaon/lycaon/internal/sourcefeed"
	"github.com/lycaon/lycaon/test/contract/invariants"
)

const sourcefeedDir = "internal/sourcefeed"

type emitSite struct{ dir, function, call string }

var userMutationSites = []emitSite{
	{"internal/projectsource", "commitSourceMutationTxWithCache", "EmitBatchTx"},
	{"internal/projectsource", "sourceSettlement.commitWithoutStore", "EmitBatch"},
}

var emitterSites = map[sourcefeed.EmitDoor][]emitSite{
	sourcefeed.DoorAgentMutation: {
		{"internal/tools/native", "commitAgentMutationTx", "EmitTx"},
		{"internal/tools/native", "commitAgentMutationWithoutStore", "Emit"},
	},
	sourcefeed.DoorEditorSave:      {{"internal/editordoc", "saveCommitter.commit", "EmitTx"}},
	sourcefeed.DoorWorkerPromotion: {{"internal/worker", "SQLStore.CommitPromotion", "EmitBatchTx"}},
	sourcefeed.DoorUserPut:         userMutationSites,
	sourcefeed.DoorUserPost:        userMutationSites,
	sourcefeed.DoorRename:          userMutationSites,
	sourcefeed.DoorDelete:          userMutationSites,
	sourcefeed.DoorCopy:            userMutationSites,
	sourcefeed.DoorReplaceApply:    userMutationSites,
}

var emitDoorRule = invariants.Invariant{
	Name:    "source-feed-doors-have-emit-sites",
	Anchors: emitDoorAnchors(),
	Check:   checkEmitDoors,
}

func emitDoorAnchors() []invariants.Anchor {
	anchors := []invariants.Anchor{invariants.Decl(sourcefeedDir, "EmitDoor")}
	seen := map[emitSite]bool{}
	for _, door := range sortedKeys(emitterSites) {
		for _, site := range emitterSites[door] {
			if !seen[site] {
				anchors = append(anchors, invariants.Func(site.dir, site.function))
				seen[site] = true
			}
		}
	}
	return anchors
}

func checkEmitDoors(r *invariants.Run) {
	declared := stringEnumValues(r, sourcefeedDir, "EmitDoor")
	feed := r.TypedPackage(sourcefeedDir).Types
	seen := map[emitSite]bool{}
	var registered []string
	for _, door := range sortedKeys(emitterSites) {
		registered = append(registered, string(door))
		sites := emitterSites[door]
		r.Expect(len(sites), "emit sites for "+string(door))
		for _, site := range sites {
			decl := r.Declaration(site.dir, site.function)
			fn := decl.Node.(*ast.FuncDecl)
			found, checked := seen[site]
			if !checked {
				info := r.TypedPackage(site.dir).Info
				target := feed.Scope().Lookup(site.call)
				decl.File.Calls(fn.Body, func(call *ast.CallExpr, _, _ string) {
					if target != nil && calledObject(info, call) == target {
						found = true
					}
				})
				seen[site] = found
			}
			if !found {
				r.ReportAt(fn.Pos(), "%s (%s) must call sourcefeed.%s", site.function, door, site.call)
			}
		}
	}
	for value, name := range declared {
		family, operation, ok := strings.Cut(value, ".")
		if !ok || family == "" || operation == "" {
			r.Report("%s = %q must contain a door family and operation", name, value)
		}
	}
	r.Diff("declared source-feed doors vs registered emit sites", sortedKeys(declared), registered)
}

func calledObject(info *types.Info, call *ast.CallExpr) types.Object {
	switch callee := ast.Unparen(call.Fun).(type) {
	case *ast.Ident:
		return info.Uses[callee]
	case *ast.SelectorExpr:
		return info.Uses[callee.Sel]
	default:
		return nil
	}
}
