package guidance_test

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/citationmd"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type surfaceCitation struct {
	Handle  string
	Path    string
	Line    int
	Excerpt string
}

type surfaceVerdict struct {
	Applicable bool
	Grounded   bool
	Code       string
	// DeclaredDiffContract cites the documented contract line justifying a
	// surface-specific class difference when Applicable is true.
	DeclaredDiffContract string
}

type rawClaimOutcome struct {
	ParsedCitations int
	RefusalCode     string
}

type citationSurface struct {
	Name         string
	EvalCitation func(roots evidence.CitationRoots, ev evidence.Ledger, c surfaceCitation) surfaceVerdict
	EvalRawInput func(roots evidence.CitationRoots, ev evidence.Ledger, raw string) rawClaimOutcome
}

func formatMarkdownCitation(c surfaceCitation) string {
	var parts []string
	if c.Handle != "" {
		tok := c.Handle
		if c.Line > 0 && c.Path == "" {
			tok = fmt.Sprintf("%s:%d", tok, c.Line)
		}
		parts = append(parts, tok)
	}
	if c.Path != "" {
		tok := c.Path
		if c.Line > 0 {
			tok = fmt.Sprintf("%s:%d", tok, c.Line)
		}
		parts = append(parts, tok)
	}
	if c.Excerpt != "" {
		parts = append(parts, fmt.Sprintf("%q", c.Excerpt))
	}
	return "[" + strings.Join(parts, " ") + "]"
}

func formatFindingRef(c surfaceCitation) (string, bool) {
	// record_finding takes a single ref string (handle or path, optional line and excerpt).
	if c.Handle != "" && c.Path != "" {
		return "", false
	}
	base := c.Handle
	if base == "" {
		base = c.Path
	}
	if base == "" {
		return "", false
	}
	if c.Line > 0 {
		base = fmt.Sprintf("%s:%d", base, c.Line)
	}
	if c.Excerpt != "" {
		base = fmt.Sprintf("%s %q", base, c.Excerpt)
	}
	return base, true
}

func citationSurfaces() []citationSurface {
	return []citationSurface{
		{
			Name: "coordinator_closeout",
			EvalCitation: func(roots evidence.CitationRoots, ev evidence.Ledger, c surfaceCitation) surfaceVerdict {
				opts := guidance.DraftParseOptions(roots, ev)
				draft := guidance.ParseCloseoutDraft("Verified behavior "+formatMarkdownCitation(c)+".", opts)
				if len(draft.Issues) > 0 {
					return surfaceVerdict{Applicable: true, Grounded: false, Code: guidance.CitationSyntaxInvalidCode}
				}
				resolved := guidance.ResolveDraftCitations(roots, "implement_investigate", draft, guidance.CloseoutEvidence{Ledger: ev}, nil)
				eval := guidance.EvaluateDraftCitations(draft, resolved)
				return surfaceVerdict{Applicable: true, Grounded: eval.Code == "" && draft.CitationCount() > 0, Code: eval.Code}
			},
			EvalRawInput: func(roots evidence.CitationRoots, ev evidence.Ledger, raw string) rawClaimOutcome {
				opts := guidance.DraftParseOptions(roots, ev)
				if guidance.IsBareJSONResponse(raw) {
					return rawClaimOutcome{RefusalCode: guidance.CitationSyntaxInvalidCode}
				}
				read := guidance.ReadCloseoutReport(raw, guidance.RetainedCloseout{}, false, opts)
				if len(read.RepairDefects) > 0 {
					return rawClaimOutcome{RefusalCode: guidance.CitationRepairInvalidCode}
				}
				if len(read.Draft.Issues) > 0 {
					return rawClaimOutcome{RefusalCode: guidance.CitationSyntaxInvalidCode}
				}
				if read.Draft.CitationCount() > 0 {
					return rawClaimOutcome{ParsedCitations: read.Draft.CitationCount()}
				}
				return rawClaimOutcome{RefusalCode: guidance.CloseoutCitationsRequiredCode("implement_investigate")}
			},
		},
		{
			Name: "mid_turn_note",
			EvalCitation: func(roots evidence.CitationRoots, ev evidence.Ledger, c surfaceCitation) surfaceVerdict {
				audit := guidance.AuditMidTurnNote(guidance.MidTurnNoteInput{
					Text:              "Observed detail " + formatMarkdownCitation(c) + ".",
					Roots:             roots,
					SurfaceID:         "implement_investigate",
					Evidence:          guidance.CloseoutEvidence{Ledger: ev},
					CitationsRequired: true,
				})
				return surfaceVerdict{Applicable: true, Grounded: audit.Outcome == guidance.MidTurnPublish, Code: audit.Code}
			},
			EvalRawInput: func(roots evidence.CitationRoots, ev evidence.Ledger, raw string) rawClaimOutcome {
				audit := guidance.AuditMidTurnNote(guidance.MidTurnNoteInput{
					Text:              raw,
					Roots:             roots,
					SurfaceID:         "implement_investigate",
					Evidence:          guidance.CloseoutEvidence{Ledger: ev},
					CitationsRequired: true,
				})
				if audit.Outcome == guidance.MidTurnWithhold {
					return rawClaimOutcome{RefusalCode: audit.Code}
				}
				opts := guidance.DraftParseOptions(roots, ev)
				draft := guidance.ParseCloseoutDraft(raw, opts)
				return rawClaimOutcome{ParsedCitations: draft.CitationCount()}
			},
		},
		{
			Name: "worker_report",
			EvalCitation: func(roots evidence.CitationRoots, ev evidence.Ledger, c surfaceCitation) surfaceVerdict {
				eval := guidance.EvaluateWorkerCitations(
					roots,
					[]guidance.WorkerFindingInput{{
						Evidence: c.Handle,
						Path:     c.Path,
						Line:     c.Line,
						Excerpt:  c.Excerpt,
					}},
					nil,
					guidance.WorkerNarrativeInput{Brief: "Checked claim."},
					ev,
				)
				return surfaceVerdict{Applicable: true, Grounded: eval.Code == "", Code: eval.Code}
			},
			EvalRawInput: func(roots evidence.CitationRoots, ev evidence.Ledger, raw string) rawClaimOutcome {
				eval := guidance.EvaluateWorkerCitations(
					roots,
					[]guidance.WorkerFindingInput{{Path: raw}},
					nil,
					guidance.WorkerNarrativeInput{Brief: "Checked claim."},
					ev,
				)
				if eval.Code != "" {
					return rawClaimOutcome{RefusalCode: eval.Code}
				}
				grounded := 0
				for _, r := range eval.Resolutions {
					if r.Verdict.Grounded() {
						grounded++
					}
				}
				return rawClaimOutcome{ParsedCitations: grounded}
			},
		},
		{
			Name: "record_finding_ref",
			EvalCitation: func(roots evidence.CitationRoots, ev evidence.Ledger, c surfaceCitation) surfaceVerdict {
				ref, ok := formatFindingRef(c)
				if !ok {
					return surfaceVerdict{Applicable: false}
				}
				eval := guidance.EvaluateFindingSummary(roots.ProjectDir, "Observed finding.", ref, ev)
				return surfaceVerdict{Applicable: true, Grounded: eval.Grounded && eval.Code == "", Code: eval.Code}
			},
			EvalRawInput: func(roots evidence.CitationRoots, ev evidence.Ledger, raw string) rawClaimOutcome {
				eval := guidance.EvaluateFindingSummary(roots.ProjectDir, "Observed finding.", raw, ev)
				if eval.Code != "" {
					return rawClaimOutcome{RefusalCode: eval.Code}
				}
				// If non-empty ref was accepted without error, verify it actually bound
				// to an observed citation rather than being silently dropped as nil.
				ref := strings.TrimSpace(raw)
				if ref != "" {
					if _, ok := evidence.ResolveHandle(ev, ref); ok {
						return rawClaimOutcome{ParsedCitations: 1}
					}
					if ev.URLSeen(ref) {
						return rawClaimOutcome{ParsedCitations: 1}
					}
					if p, _, ok := evidence.SplitPathLineToken(ref); ok && evidence.LedgerPathKey(roots, p, ev.ByPath) != "" {
						return rawClaimOutcome{ParsedCitations: 1}
					}
				}
				return rawClaimOutcome{ParsedCitations: 0, RefusalCode: ""}
			},
		},
		{
			Name: "submit_verdict",
			EvalCitation: func(roots evidence.CitationRoots, ev evidence.Ledger, c surfaceCitation) surfaceVerdict {
				eval := guidance.EvaluateVerdictGrounding(
					roots,
					guidance.CloseoutEvidence{Ledger: ev},
					[]api.CitationGroundingCitedEvidence{{
						Handle:  c.Handle,
						Path:    c.Path,
						Line:    c.Line,
						Excerpt: c.Excerpt,
					}},
					nil,
					nil,
				)
				return surfaceVerdict{Applicable: true, Grounded: eval.Code == "", Code: eval.Code}
			},
			EvalRawInput: func(roots evidence.CitationRoots, ev evidence.Ledger, raw string) rawClaimOutcome {
				eval := guidance.EvaluateVerdictGrounding(
					roots,
					guidance.CloseoutEvidence{Ledger: ev},
					[]api.CitationGroundingCitedEvidence{{Path: raw}},
					nil,
					nil,
				)
				if eval.Code != "" {
					return rawClaimOutcome{RefusalCode: eval.Code}
				}
				return rawClaimOutcome{ParsedCitations: len(eval.Grounding.CitedEvidence)}
			},
		},
		{
			Name: "workflow_run_report",
			EvalCitation: func(roots evidence.CitationRoots, ev evidence.Ledger, c surfaceCitation) surfaceVerdict {
				doc := guidance.WorkflowRunReportDocument{
					Headline: "Workflow report",
					Summary:  "Report summary.",
					Findings: []guidance.CoordinatorFinding{{
						ID:    "F1",
						Title: "Finding",
						Where: []guidance.CoordinatorCitedEvidence{{
							Evidence: c.Handle,
							Path:     c.Path,
							Line:     c.Line,
							Excerpt:  c.Excerpt,
						}},
					}},
				}
				payload, err := json.Marshal(doc)
				if err != nil {
					panic(err)
				}
				raw := "Workflow narrative.\n\n```json\n" + string(payload) + "\n```"
				opts := guidance.DraftParseOptions(roots, ev)
				read := guidance.ReadCloseoutReport(raw, guidance.RetainedCloseout{}, true, opts)
				if issue, ok := guidance.ReportFenceUnreadable(read.Unread); ok {
					return surfaceVerdict{Applicable: true, Grounded: false, Code: issue.Code}
				}
				resolved := guidance.ResolveDraftCitations(roots, "synthesis", read.Draft, guidance.CloseoutEvidence{Ledger: ev}, nil)
				eval := guidance.EvaluateDraftCitations(read.Draft, resolved)
				return surfaceVerdict{Applicable: true, Grounded: eval.Code == "" && read.Draft.CitationCount() > 0, Code: eval.Code}
			},
			EvalRawInput: func(roots evidence.CitationRoots, ev evidence.Ledger, raw string) rawClaimOutcome {
				opts := guidance.DraftParseOptions(roots, ev)
				read := guidance.ReadCloseoutReport(raw, guidance.RetainedCloseout{}, true, opts)
				if issue, ok := guidance.ReportFenceUnreadable(read.Unread); ok {
					return rawClaimOutcome{RefusalCode: issue.Code}
				}
				if len(read.RepairDefects) > 0 {
					return rawClaimOutcome{RefusalCode: guidance.CitationRepairInvalidCode}
				}
				if len(read.Draft.Issues) > 0 {
					return rawClaimOutcome{RefusalCode: guidance.CitationSyntaxInvalidCode}
				}
				if read.Draft.CitationCount() > 0 {
					return rawClaimOutcome{ParsedCitations: read.Draft.CitationCount()}
				}
				return rawClaimOutcome{RefusalCode: guidance.CloseoutCitationsRequiredCode("synthesis")}
			},
		},
		{
			Name: "repair_directive",
			EvalCitation: func(roots evidence.CitationRoots, ev evidence.Ledger, c surfaceCitation) surfaceVerdict {
				opts := guidance.DraftParseOptions(roots, ev)
				initial := guidance.ParseCloseoutDraft("Verified behavior [read#999].", opts)
				retained := guidance.RetainedCloseout{
					Active:    true,
					Draft:     initial,
					RepairIDs: map[string]int{"c1": 1},
				}
				tok := strings.TrimSuffix(strings.TrimPrefix(formatMarkdownCitation(c), "["), "]")
				repairInput := "[cite: c1 => " + tok + "]"
				read := guidance.ReadCloseoutReport(repairInput, retained, false, opts)
				if len(read.RepairDefects) > 0 {
					return surfaceVerdict{Applicable: true, Grounded: false, Code: guidance.CitationRepairInvalidCode}
				}
				if len(read.Draft.Issues) > 0 {
					return surfaceVerdict{Applicable: true, Grounded: false, Code: guidance.CitationSyntaxInvalidCode}
				}
				resolved := guidance.ResolveDraftCitations(roots, "implement_investigate", read.Draft, guidance.CloseoutEvidence{Ledger: ev}, nil)
				eval := guidance.EvaluateDraftCitations(read.Draft, resolved)
				return surfaceVerdict{Applicable: true, Grounded: eval.Code == "" && read.Draft.CitationCount() > 0, Code: eval.Code}
			},
			EvalRawInput: func(roots evidence.CitationRoots, ev evidence.Ledger, raw string) rawClaimOutcome {
				opts := guidance.DraftParseOptions(roots, ev)
				initial := guidance.ParseCloseoutDraft("Verified behavior [read#999].", opts)
				retained := guidance.RetainedCloseout{
					Active:    true,
					Draft:     initial,
					RepairIDs: map[string]int{"c1": 1},
				}
				read := guidance.ReadCloseoutReport(raw, retained, false, opts)
				if len(read.RepairDefects) > 0 {
					return rawClaimOutcome{RefusalCode: guidance.CitationRepairInvalidCode}
				}
				// Check whether the repair directive actually parsed a repair or citation.
				next := guidance.ParseCloseoutDraft(raw, opts)
				if next.HasRepairDirective() || next.CitationCount() > 0 {
					return rawClaimOutcome{ParsedCitations: max(1, next.CitationCount())}
				}
				if len(next.Issues) > 0 {
					// Malformed node was parsed as an issue; if ReadCloseoutReport
					// returned zero RepairDefects, it was silently dropped!
					return rawClaimOutcome{ParsedCitations: 0, RefusalCode: ""}
				}
				if citationmd.Narrative(next.Parsed) == "" {
					return rawClaimOutcome{ParsedCitations: 0, RefusalCode: ""}
				}
				return rawClaimOutcome{RefusalCode: guidance.CitationRepairInvalidCode}
			},
		},
	}
}

// TestCitationSurfacesAgreeInvariant proves GB10 I1: for the same ledger and
// citation, every citation-accepting surface gives the same grounded or
// not-grounded class (first cases: W03-10, GB06-F1, V01-07, plus generated).
func TestCitationSurfacesAgreeInvariant(t *testing.T) {
	t.Parallel()
	roots := evidence.CitationRoots{ProjectDir: "/tmp/gb10-i1"}
	surfaces := citationSurfaces()

	obs, err := evidence.ConfirmNumberedWindow("src/app.go", "1\tpackage app\n2\tfunc Run() bool { return true }\n3\t", "resp-1", "deliv-1")
	testutil.FailErr(t, "ConfirmNumberedWindow", err)
	deliveredRec := obs.Record()
	deliveredRec.Handle = "read#1"
	deliveredLedger := evidence.AssembleLedger([]evidence.Record{deliveredRec})

	commandLedger := evidence.AssembleLedger([]evidence.Record{{
		Handle:     "command#1",
		Kind:       "command",
		Shape:      evidence.ShapeCommand,
		Fidelity:   evidence.FidelityOpaque,
		SourceTool: "command",
		Body:       []string{"status_code: 1", "FAIL: unit_x"},
	}})

	type agreementCase struct {
		name         string
		ev           evidence.Ledger
		cite         surfaceCitation
		wantGrounded bool
	}

	cases := []agreementCase{
		// First case W03-10: undelivered line 90 on a 3-line numbered delivery.
		{
			name:         "W03-10_undelivered_line_on_numbered_delivery",
			ev:           deliveredLedger,
			cite:         surfaceCitation{Path: "src/app.go", Line: 90},
			wantGrounded: false,
		},
		{
			name:         "W03-10_undelivered_line_with_handle_on_numbered_delivery",
			ev:           deliveredLedger,
			cite:         surfaceCitation{Handle: "read#1", Line: 90},
			wantGrounded: false,
		},
		// First case GB06-F1: path:line with fabricated excerpt.
		{
			name:         "GB06-F1_path_line_fabricated_excerpt",
			ev:           deliveredLedger,
			cite:         surfaceCitation{Path: "src/app.go", Line: 2, Excerpt: "func Fabricated()"},
			wantGrounded: false,
		},
		// First case V01-07: command handle with fabricated excerpt.
		{
			name:         "V01-07_command_handle_fabricated_excerpt",
			ev:           commandLedger,
			cite:         surfaceCitation{Handle: "command#1", Excerpt: "ALL TESTS PASSED"},
			wantGrounded: false,
		},
		// Positive controls: supported citations ground everywhere.
		{
			name:         "control_supported_path_line",
			ev:           deliveredLedger,
			cite:         surfaceCitation{Path: "src/app.go", Line: 2},
			wantGrounded: true,
		},
		{
			name:         "control_supported_handle_excerpt",
			ev:           commandLedger,
			cite:         surfaceCitation{Handle: "command#1", Excerpt: "FAIL: unit_x"},
			wantGrounded: true,
		},
		{
			name:         "control_unobserved_path_line",
			ev:           deliveredLedger,
			cite:         surfaceCitation{Path: "src/never_read.go", Line: 5},
			wantGrounded: false,
		},
		{
			name:         "control_unknown_handle",
			ev:           deliveredLedger,
			cite:         surfaceCitation{Handle: "read#99"},
			wantGrounded: false,
		},
	}

	// Deterministic generated cases over varied paths, lines, and excerpts.
	rng := rand.New(rand.NewPCG(20261127, 0x11))
	for i := range 10 {
		start := 1 + rng.IntN(20)
		end := start + 2
		path := fmt.Sprintf("internal/gen/s_%d.go", i)
		tok := fmt.Sprintf("val_%d_%x", i, rng.Uint32())
		numbered := fmt.Sprintf("%d\tpackage gen\n%d\tfunc Token_%s() {}\n%d\t", start, start+1, tok, end)
		genObs, err := evidence.ConfirmNumberedWindow(path, numbered, fmt.Sprintf("resp-%d", i), fmt.Sprintf("deliv-%d", i))
		testutil.FailErr(t, "ConfirmNumberedWindow", err)
		genRec := genObs.Record()
		genRec.Handle = "read#1"
		ev := evidence.AssembleLedger([]evidence.Record{genRec})
		cases = append(cases,
			agreementCase{
				name:         fmt.Sprintf("gen_%d_supported_excerpt", i),
				ev:           ev,
				cite:         surfaceCitation{Path: path, Line: start + 1, Excerpt: fmt.Sprintf("func Token_%s() {}", tok)},
				wantGrounded: true,
			},
			agreementCase{
				name:         fmt.Sprintf("gen_%d_out_of_range_line", i),
				ev:           ev,
				cite:         surfaceCitation{Path: path, Line: end + 25},
				wantGrounded: false,
			},
			agreementCase{
				name:         fmt.Sprintf("gen_%d_handle_contradicting_excerpt", i),
				ev:           ev,
				cite:         surfaceCitation{Handle: "read#1", Line: start + 1, Excerpt: "func WrongToken() {}"},
				wantGrounded: false,
			},
		)
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			results := make(map[string]surfaceVerdict, len(surfaces))
			for _, surface := range surfaces {
				v := surface.EvalCitation(roots, tc.ev, tc.cite)
				if !v.Applicable {
					continue
				}
				results[surface.Name] = v
				if v.Grounded != tc.wantGrounded && v.DeclaredDiffContract == "" {
					t.Errorf("surface %s grounded=%v (code=%q), want %v for citation %+v",
						surface.Name, v.Grounded, v.Code, tc.wantGrounded, tc.cite)
				}
			}
			// Cross-surface agreement check: every applicable surface without a
			// declared contract difference must agree on grounded class.
			var baselineName string
			var baselineGrounded bool
			for _, surface := range surfaces {
				v, ok := results[surface.Name]
				if !ok || v.DeclaredDiffContract != "" {
					continue
				}
				if baselineName == "" {
					baselineName, baselineGrounded = surface.Name, v.Grounded
					continue
				}
				if v.Grounded != baselineGrounded {
					t.Errorf("surface disagreement on %+v: %s grounded=%v (code=%q) vs %s grounded=%v",
						tc.cite, surface.Name, v.Grounded, v.Code, baselineName, baselineGrounded)
				}
			}
		})
	}
}
