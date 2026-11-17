package forbiddencalls

import (
	"go/ast"
	"strings"

	"github.com/lycaon/lycaon/test/contract/laws"
)

const datamarkPkg = "github.com/lycaon/lycaon/internal/datamark"

// coordinatorSurfaceLiterals are surface ids and mode refs declared by the
// flow catalog.
var coordinatorSurfaceLiterals = []string{
	"implement_routing", "implement_dispatch", "implement_synthesis", "implement_investigate", "implement_overlay_promote",
	"implement_park", "workflow_compose", "plan_stub", "plan_research", "plan_approve", "await_user", "plan_execute",
	"implement-investigate", "implement-overlay-promote", "implement-synthesis", "implement-dispatch", "implement-routing",
	"implement-park", "compose",
}

// stablePrefixFuncs build the cached prompt prefix; whole files are named by
// their path.
var stablePrefixFuncs = []struct{ dir, name string }{
	{"internal/coordinator/assembly", "turnContextAssembler.prependTransitionInject"},
	{"internal/coordinator/assembly", "markPromptCacheBreakpoints"},
	{"internal/coordinator/assembly", "turnContextAssembler.appendPreHistorySystemInjects"},
}

var stablePrefixFiles = []string{
	"internal/coordinator/assembly/tripartite_prompt.go",
	"internal/coordinator/inject/execution_mode_prompt.go",
}

var prompts = []laws.Rule{{
	// The retrieval delimiter has one implementation; a second would be a
	// second boundary for the model to learn.
	Name:    "retrieval-marker-has-one-implementation",
	Anchors: []laws.Anchor{laws.Func("internal/datamark", "Frame")},
	Check: func(r *laws.Run) {
		for _, f := range r.GoFiles(laws.Production, "internal") {
			if !laws.Under(f, "internal/datamark") && strings.ContainsAny(string(f.Src), "⟪⟫") {
				r.Report("%s writes the retrieval delimiter outside internal/datamark", f.Rel)
			}
		}
	},
}, {
	// Marking happens where content becomes a message; a handler that marks
	// its own output caches a nonce and is wrapped twice.
	Name:    "tool-handlers-do-not-mark-their-own-output",
	Anchors: []laws.Anchor{laws.Func("internal/datamark", "Frame")},
	Check: func(r *laws.Run) {
		for _, f := range r.GoFiles(laws.Production, "internal/webresearch", "internal/mcp", "internal/tools") {
			f.Calls(f.AST, func(call *ast.CallExpr, pkg, name string) {
				if pkg == datamarkPkg && name == "Frame" {
					r.ReportAt(call.Pos(), "a tool handler marks its own output; marking belongs to the message seam")
				}
			})
		}
	},
}, {
	// Surface ids and mode refs come from the flow catalog; coordinator code
	// names them only where the catalog is loaded and evaluated.
	Name:    "coordinator-flow-surface-literal-containment",
	Anchors: []laws.Anchor{laws.Package("internal/coordinator/surface")},
	Exempt:  surfaceLiteralFiles(),
	Check: func(r *laws.Run) {
		for _, f := range r.GoFiles(laws.Production, "internal/coordinator") {
			laws.StringLits(f.AST, func(lit *ast.BasicLit, value string) {
				if contains(coordinatorSurfaceLiterals, value) && !r.Exempt(f.Rel) {
					r.ReportAt(lit.Pos(), "surface or mode literal %q outside the flow catalog code", value)
				}
			})
		}
	},
}, {
	// The stable prompt prefix is cache-keyed: no clock, randomness or fresh
	// ids, and no map iteration order.
	Name:    "stable-prefix-has-no-silent-invalidators",
	Anchors: stablePrefixAnchors(),
	Check: func(r *laws.Run) {
		scan := func(f *laws.GoFile, fn *ast.FuncDecl) {
			f.Calls(fn.Body, func(call *ast.CallExpr, pkg, name string) {
				switch {
				case pkg == "math/rand" || pkg == "math/rand/v2" || pkg == "crypto/rand",
					pkg == "time" && (name == "Now" || name == "Since"),
					pkg == "github.com/google/uuid" && strings.HasPrefix(name, "New"):
					r.ReportAt(call.Pos(), "%s calls %s.%s on the stable prefix", fn.Name.Name, pkg, name)
				}
			})
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				if loop, ok := n.(*ast.RangeStmt); ok && loop.Value != nil {
					if id, ok := loop.X.(*ast.Ident); ok && (id.Name == "vars" || id.Name == "surfaceVars") {
						r.ReportAt(loop.Pos(), "%s ranges over %s; iterate a sorted key copy", fn.Name.Name, id.Name)
					}
				}
				return true
			})
		}
		for _, target := range stablePrefixFuncs {
			scan(r.Declaration(target.dir, target.name).File, r.FuncDecl(target.dir, target.name))
		}
		for _, rel := range stablePrefixFiles {
			f := r.GoFile(rel)
			f.Funcs(func(fn *ast.FuncDecl) { scan(f, fn) })
		}
	},
}}

func surfaceLiteralFiles() []laws.Exemption {
	files := []string{
		"internal/coordinator/surface/profile.go",
		"internal/coordinator/surface/flow_parity.go",
		"internal/coordinator/surface/envelope.go",
		"internal/coordinator/surface/execution_mode_state.go",
		"internal/coordinator/assembly/prompt_dump.go",
		"internal/coordinator/promptloop/run_stages.go",
	}
	out := make([]laws.Exemption, 0, len(files))
	for _, file := range files {
		out = append(out, laws.Exemption{Key: file, Reason: "loads, evaluates or dispatches on the flow catalog's surfaces"})
	}
	return out
}

func stablePrefixAnchors() []laws.Anchor {
	out := goPaths(stablePrefixFiles)
	for _, target := range stablePrefixFuncs {
		out = append(out, laws.Func(target.dir, target.name))
	}
	return out
}

// synthesisEvidenceFiles assemble the worker union a synthesis reads.
var synthesisEvidenceFiles = []string{
	"internal/survey/synthesis_snapshot.go",
	"internal/survey/synthesis_curate.go",
	"internal/survey/synthesis_evidence.go",
	"internal/workflow/review/fanout_evidence.go",
	"internal/session/closeoutassembly/synthesis_evidence.go",
}

var promptInputs = []laws.Rule{{
	// The prompt cache keys on rendered content; a render that reads the
	// clock misses every time. A timestamp is an explicit parameter.
	Name:    "prompt-rendering-has-no-wall-clock-calls",
	Anchors: []laws.Anchor{laws.Package("internal/prompts")},
	Check: func(r *laws.Run) {
		for _, f := range r.GoFiles(laws.Production, "internal/prompts") {
			f.Selectors(f.AST, func(sel *ast.SelectorExpr, pkg, name string) {
				if pkg == "time" && (name == "Now" || name == "Since" || name == "Until") {
					r.ReportAt(sel.Pos(), "time.%s on the prompt render path; take the time as a parameter", name)
				}
			})
		}
	},
}, {
	// Delegation spawns and prompts children through its ports, not the
	// session service directly.
	Name:    "delegation-does-not-spawn-through-sessions",
	Anchors: []laws.Anchor{laws.GoPath("internal/delegation/manager_impl.go")},
	Check: func(r *laws.Run) {
		f := r.GoFile("internal/delegation/manager_impl.go")
		f.Calls(f.AST, func(call *ast.CallExpr, _, _ string) {
			if name := laws.CalleeName(call); name == "SpawnChild" || name == "Prompt" {
				r.ReportAt(call.Pos(), "delegation calls %s on sessions directly", name)
			}
		})
	},
}, {
	// Synthesis reads the whole worker union; summarizing or compacting it
	// first drops evidence the verdict cites.
	Name:    "synthesis-evidence-is-not-summarized",
	Anchors: goPaths(synthesisEvidenceFiles),
	Check: func(r *laws.Run) {
		for _, rel := range synthesisEvidenceFiles {
			f := r.GoFile(rel)
			f.Calls(f.AST, func(call *ast.CallExpr, _, _ string) {
				if name := laws.CalleeName(call); name == "Summarize" || name == "Compact" {
					r.ReportAt(call.Pos(), "%s on the worker union", name)
				}
			})
		}
	},
}}
