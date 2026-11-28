package forbiddencalls

import (
	"go/ast"

	"github.com/lycaon/lycaon/test/contract/invariants"
)

// citationSurfaceFiles are the production files implementing the seven
// citation-accepting surfaces registered in GB10 I1.
var citationSurfaceFiles = map[string]bool{
	"internal/guidance/evaluate_worker_citations.go": true,
	"internal/guidance/finding_grounding.go":         true,
	"internal/guidance/closeout_draft.go":            true,
	"internal/guidance/closeout_read.go":             true,
	"internal/guidance/mid_turn_note.go":             true,
}

var citationSurfaces = []invariants.Invariant{{
	// Every caller of citation resolution entry points (evidence.Resolve,
	// evidence.ResolveObservation, citationmd.Parse) is either a registered
	// citation surface in the I1 surface table or an explicitly reasoned
	// non-surface exemption.
	Name: "citation-resolution-callers-are-registered-surfaces",
	Anchors: []invariants.Anchor{
		invariants.Func("internal/evidence", "Resolve"),
		invariants.Func("internal/evidence", "ResolveObservation"),
		invariants.Func("internal/citationmd", "Parse"),
	},
	Exempt: []invariants.Exemption{
		{
			Key:    "internal/llm/curator_impl.go",
			Reason: "context-curation selector, not an agent-facing citation surface; filters to matched or excerpt-less traced lines and materializes observed bytes",
		},
		{
			Key:    "internal/guidance/closeout_present_markdown.go",
			Reason: "presentation renderer rewriting already-validated synthesis Markdown into display links",
		},
		{
			Key:    "internal/coordinator/guard/finish_guard.go",
			Reason: "finish guard projecting Markdown narrative to detect empty-text responses",
		},
		{
			Key:    "internal/coordinator/promptloop/ledger_closeout_emit.go",
			Reason: "host-assembled closeout extracting observed URLs from synthesis Markdown",
		},
		{
			Key:    "internal/coordinator/promptloop/provisional_turn.go",
			Reason: "provisional turn projecting citation nodes out of streamed assistant text",
		},
		{
			Key:    "internal/session/policyfacts/reproduced_source.go",
			Reason: "policy fact extractor reading code-fence spans for reproduced-source detection",
		},
	},
	Check: func(r *invariants.Run) {
		calls := 0
		for _, f := range r.GoFiles(invariants.Production, invariants.Source...) {
			f.Calls(f.AST, func(call *ast.CallExpr, pkg, name string) {
				isEvidenceResolve := pkg == "github.com/lycaon/lycaon/internal/evidence" && (name == "Resolve" || name == "ResolveObservation")
				isCitationParse := pkg == "github.com/lycaon/lycaon/internal/citationmd" && name == "Parse"
				if !isEvidenceResolve && !isCitationParse {
					return
				}
				calls++
				if citationSurfaceFiles[f.Rel] || r.Exempt(f.Rel) {
					return
				}
				r.ReportAt(call.Pos(), "%s.%s called outside registered citation surfaces; register the surface in GB10 I1 or add a reasoned exemption", pkg, name)
			})
		}
		r.Expect(calls, "calls to citation resolution entry points")
	},
}}
