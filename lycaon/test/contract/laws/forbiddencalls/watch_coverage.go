package forbiddencalls

import (
	"fmt"
	"go/ast"
	"go/token"
	"path"
	"strings"

	"github.com/lycaon/lycaon/test/contract/laws"
)

var watchCoverage = []laws.Rule{{
	// A truncated watch stops the epoch moving without reporting it, so epoch
	// equality is trusted only after consulting repochange.Coverage or
	// DirWatched at the grain of the claim.
	Name:    "epoch-fast-paths-consult-watch-coverage",
	Anchors: []laws.Anchor{laws.Func("internal/repochange", "EpochCurrent")},
	Exempt:  epochTrustExemptions,
	Check: func(r *laws.Run) {
		var funcs []*epochFunc
		for _, f := range r.GoFiles(laws.Production, "internal", "cmd", "pkg", "config") {
			if isDoorTestSupport(f) {
				continue
			}
			f.Funcs(func(fn *ast.FuncDecl) { funcs = append(funcs, inspectEpochFunc(f.Rel, fn)) })
		}
		consults := coverageConsultingFuncs(funcs)
		for _, fn := range funcs {
			if fn.comparesEpochs && !r.Exempt(fn.key) && !consults[fn] {
				r.Report("%s trusts epoch equality without repochange.Coverage or DirWatched", fn.key)
			}
		}
	},
}}

type epochFunc struct {
	key            string // repo-relative file:FuncName
	name           string // function name alone, for call resolution
	pkg            string // repo-relative package directory
	comparesEpochs bool
	consults       bool
	calls          []string
}

func inspectEpochFunc(rel string, fn *ast.FuncDecl) *epochFunc {
	info := &epochFunc{key: fmt.Sprintf("%s:%s", rel, fn.Name.Name), name: fn.Name.Name,
		pkg: path.Dir(rel)}
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.BinaryExpr:
			if (node.Op == token.EQL || node.Op == token.NEQ) &&
				!isConstantEpochOperand(node.X) && !isConstantEpochOperand(node.Y) &&
				(namesEpoch(node.X) || namesEpoch(node.Y)) {
				info.comparesEpochs = true
			}
		case *ast.CallExpr:
			name, pkg := epochCallName(node)
			switch {
			case pkg == "repochange" && name == "EpochCurrent":
				info.comparesEpochs = true
			case pkg == "repochange" && (name == "Coverage" || name == "DirWatched"):
				info.consults = true
			case name != "":
				info.calls = append(info.calls, name)
			}
		}
		return true
	})
	return info
}

// coverageConsultingFuncs follows local calls and unambiguous cross-package names to Coverage.
func coverageConsultingFuncs(funcs []*epochFunc) map[*epochFunc]bool {
	byPkg := map[string]*epochFunc{}
	byName := map[string][]*epochFunc{}
	for _, fn := range funcs {
		byPkg[fn.pkg+"."+fn.name] = fn
		byName[fn.name] = append(byName[fn.name], fn)
	}
	resolve := func(from *epochFunc, callee string) *epochFunc {
		if target, ok := byPkg[from.pkg+"."+callee]; ok {
			return target
		}
		if candidates := byName[callee]; len(candidates) == 1 {
			return candidates[0]
		}
		return nil
	}
	consults := map[*epochFunc]bool{}
	for _, fn := range funcs {
		if fn.consults {
			consults[fn] = true
		}
	}
	for changed := true; changed; {
		changed = false
		for _, fn := range funcs {
			if consults[fn] {
				continue
			}
			for _, callee := range fn.calls {
				if target := resolve(fn, callee); target != nil && consults[target] {
					consults[fn] = true
					changed = true
					break
				}
			}
		}
	}
	return consults
}

func epochCallName(call *ast.CallExpr) (name, pkg string) {
	switch fun := call.Fun.(type) {
	case *ast.Ident:
		return fun.Name, ""
	case *ast.SelectorExpr:
		if id, ok := fun.X.(*ast.Ident); ok {
			return fun.Sel.Name, id.Name
		}
		return fun.Sel.Name, ""
	}
	return "", ""
}

// Presence checks do not compare observation generations. In particular, a
// parsed Unix timestamp pointer being non-nil says nothing about watch freshness.
func isConstantEpochOperand(expr ast.Expr) bool {
	expr = ast.Unparen(expr)
	if _, ok := expr.(*ast.BasicLit); ok {
		return true
	}
	ident, ok := expr.(*ast.Ident)
	return ok && ident.Name == "nil"
}

// namesEpoch ignores package qualifiers when identifying epoch operands.
func namesEpoch(expr ast.Expr) bool {
	found := false
	ast.Inspect(expr, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.SelectorExpr:
			if strings.Contains(strings.ToLower(node.Sel.Name), "epoch") {
				found = true
				return false
			}
			if _, isPkg := node.X.(*ast.Ident); isPkg {
				return false
			}
		case *ast.Ident:
			if strings.Contains(strings.ToLower(node.Name), "epoch") {
				found = true
				return false
			}
		}
		return true
	})
	return found
}

// epochTrustExemptions compare epochs that are not watch-observation freshness.
var epochTrustExemptions = []laws.Exemption{
	{Key: "internal/app/sessions/credential_index.go:changed", Reason: "checks contiguous repochange.Event sequence to fall back to a full scan on any gap; EnsureProject consults repochange.Coverage"},
	{Key: "internal/briefingstore/store.go:checkEpoch", Reason: "compares SQLite briefing cache_epoch invalidation counter, not filesystem watch state"},
	{Key: "internal/briefingstore/targets.go:Resolve", Reason: "compares SQLite briefing cache_epoch invalidation counter, not filesystem watch state"},
	{Key: "internal/browser/timeline_record.go:sampleRegions", Reason: "compares in-page DOM geometry counter, not filesystem watch state"},
	{Key: "internal/editordoc/agent_presence.go:anchorSpans", Reason: "compares CRDT document replica identity epoch, not filesystem watch state"},
	{Key: "internal/editordoc/replica_commit.go:SubmitReplica", Reason: "compares CRDT replica identity epoch, not filesystem watch state"},
	{Key: "internal/editordoc/replica_commit.go:acceptReplica", Reason: "compares CRDT replica identity epoch, not filesystem watch state"},
	{Key: "internal/editordoc/service.go:projectionVector", Reason: "compares retained CRDT replica identity to select a differential update; source freshness is established by the observed document open"},
	{Key: "internal/editordoc/rewind_semantic.go:semanticRewindUndos", Reason: "compares retained CRDT identity epochs"},
	{Key: "internal/sourceledger/publication_authorship.go:recordPublicationAuthorship", Reason: "compares CRDT identity epochs on retained document versions, not filesystem observation freshness"},
	{Key: "internal/sourceledger/recorded_text_authors.go:recordedTextAuthors", Reason: "compares CRDT identity epochs on immutable retained versions, not filesystem observation freshness"},
	{Key: "internal/sourceledger/recorded_text_edits.go:recordedTextEdits", Reason: "compares CRDT identity epochs on immutable retained versions, not filesystem observation freshness"},
	{Key: "internal/upgradefixture/editor_history.go:verifyEditorCore", Reason: "verifies retained CRDT replica head epoch in upgrade rehearsal fixture against recorded evidence"},
	{Key: "internal/repochange/notify.go:EpochCurrent", Reason: "the primitive itself; coverage is a separate question its callers ask"},
	{Key: "internal/sourcecatalog/refresh.go:buildRoot", Reason: "the comparison rejects a catalog build whose tree moved under it; it reuses nothing, and any consumer of the stamped generation is the one that must trust it"},
	{Key: "internal/sourcecatalog/store_core.go:settleEpoch", Reason: "records the epoch a completed pass observed and queues known concurrent changes; openStore owns reuse and consults coverage with a bounded validation age"},
	{Key: "internal/sourceledger/inventory.go:InventoryState", Reason: "reports whether the completed inventory matches the requested one; the decision to refresh belongs to EnsureInventory, which consults coverage"},
}
