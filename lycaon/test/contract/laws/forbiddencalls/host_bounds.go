package forbiddencalls

import (
	"go/ast"
	"strings"

	"github.com/lycaon/lycaon/test/contract/laws"
)

const gotreesitterPkg = "github.com/odvcencio/gotreesitter"

// parsingTaggerMethods parse internally and bypass the budget like a raw
// parser. TagTree tags a tree the caller already parsed, so it is absent.
var parsingTaggerMethods = []string{"Tag", "TagUTF16", "TagUTF16Bytes", "TagIncremental", "TagIncrementalUTF16", "TagIncrementalUTF16Bytes"}

// timeoutClaims are functions whose callers rely on their own internal
// deadline, as their contextcheck exemptions say.
var timeoutClaims = []struct{ dir, name string }{
	{"internal/project", "gitRemoteHash"},
	{"internal/worker", "sqlQueueCore.Get"},
	{"internal/llm", "fetchDiscoveredModels"},
}

var hostBounds = []laws.Rule{{
	// Tree-sitter parses go through internal/tsparse, which applies the budget.
	Name:    "parses-go-through-the-bounded-seam",
	Anchors: []laws.Anchor{laws.Func("internal/tsparse", "Parse")},
	Check: func(r *laws.Run) {
		for _, f := range r.GoFiles(laws.Production, laws.Source...) {
			if laws.Under(f, "internal/tsparse") || !f.Imports(gotreesitterPkg) {
				continue
			}
			f.Calls(f.AST, func(call *ast.CallExpr, pkg, name string) {
				sel, ok := call.Fun.(*ast.SelectorExpr)
				switch {
				case pkg == gotreesitterPkg && name == "NewParser":
					r.ReportAt(call.Pos(), "gotreesitter.NewParser is unbounded; use tsparse.Parse")
				case ok && contains(parsingTaggerMethods, sel.Sel.Name):
					r.ReportAt(call.Pos(), "Tagger.%s parses unbounded; parse with tsparse.Parse and use TagTree", sel.Sel.Name)
				}
			})
		}
	},
}, {
	// A function its callers trust to bound itself constructs its deadline.
	Name:    "bounded-timeout-claims-construct-a-deadline",
	Anchors: timeoutClaimAnchors(),
	Check: func(r *laws.Run) {
		for _, claim := range timeoutClaims {
			fn := r.FuncDecl(claim.dir, claim.name)
			f := r.Declaration(claim.dir, claim.name).File
			bounded := false
			f.Calls(fn.Body, func(_ *ast.CallExpr, pkg, name string) {
				bounded = bounded || pkg == "context" && (name == "WithTimeout" || name == "WithDeadline")
			})
			if !bounded {
				r.ReportAt(fn.Pos(), "%s claims an internal timeout but constructs no deadline", claim.name)
			}
		}
	},
}, {
	// Chrome stays in its launcher's process group, so a hard-killed parent's
	// group sweep reaches it.
	Name:    "browser-keeps-its-launcher-process-group",
	Anchors: []laws.Anchor{laws.Package("internal/browser"), laws.Package("internal/browserengine")},
	Check: func(r *laws.Run) {
		for _, f := range r.GoFiles(laws.AnyGo, "internal/browser", "internal/browserengine") {
			ast.Inspect(f.AST, func(n ast.Node) bool {
				if sel, ok := n.(*ast.SelectorExpr); ok && contains([]string{"Setpgid", "Pdeathsig", "CREATE_NEW_PROCESS_GROUP"}, sel.Sel.Name) {
					r.ReportAt(sel.Pos(), "sets %s; chrome stays in its launcher's group", sel.Sel.Name)
				}
				return true
			})
		}
	},
}, {
	// The browser resolves from the environment, the bundle or the managed
	// cache and refuses otherwise; it never searches PATH.
	Name:    "browser-never-resolves-from-path",
	Anchors: []laws.Anchor{laws.Package("internal/browser"), laws.Package("internal/browserengine")},
	Check: func(r *laws.Run) {
		for _, f := range r.GoFiles(laws.AnyGo, "internal/browser", "internal/browserengine") {
			f.Calls(f.AST, func(call *ast.CallExpr, _, _ string) {
				if strings.HasSuffix(laws.CalleeName(call), "LookPath") {
					r.ReportAt(call.Pos(), "%s searches PATH", laws.CalleeName(call))
				}
			})
		}
	},
}}

func timeoutClaimAnchors() []laws.Anchor {
	out := make([]laws.Anchor, 0, len(timeoutClaims))
	for _, claim := range timeoutClaims {
		out = append(out, laws.Func(claim.dir, claim.name))
	}
	return out
}
