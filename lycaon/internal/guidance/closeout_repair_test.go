package guidance

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestCitationRepairRetainsValidReferencesAndOffersExactToolReceipts(t *testing.T) {
	ev := CloseoutEvidence{Ledger: evidence.AssembleLedger([]evidence.Record{
		{Handle: "command#37", Kind: "command", Shape: evidence.ShapeCommand, SourceTool: "command"},
		{Handle: "git_receipt#1", Kind: "git_receipt", Shape: evidence.ShapeCommand, SourceTool: "git_commit", Path: "data/cards.yaml"},
		{Handle: "read#1", Kind: "read", Shape: evidence.ShapeFileRegion, Path: "unrelated.go"},
	})}
	report := CoordinatorCompletionReport{Synthesis: "The merge is committed.", CitedEvidence: []CoordinatorCitedEvidence{{Evidence: "command#37"}, {Evidence: "git_commit#7"}}}
	data := CloseoutRepairHintData(evidence.CitationRoots{}, "implement_investigate", report, ev, []string{"git_commit#7"})
	var retained CoordinatorCompletionReport
	testutil.FailErr(t, "decode retained citations", json.Unmarshal([]byte(data["retained_citations"].(string)), &retained))
	if len(retained.CitedEvidence) != 1 || retained.CitedEvidence[0].Evidence != "command#37" {
		t.Fatalf("retained = %+v", retained)
	}
	var choices []citationRepairObservation
	testutil.FailErr(t, "decode repair choices", json.Unmarshal([]byte(data["repair_observations"].(string)), &choices))
	if len(choices) != 1 || choices[0].Evidence != "git_receipt#1" {
		t.Fatalf("repair choices = %+v", choices)
	}
}

func TestHistoricalCitationKeepsExactObservationAndNamespace(t *testing.T) {
	old := evidence.Record{Handle: "read#1", Kind: "read", Shape: evidence.ShapeFileRegion, Path: "data/cards.yaml", Body: []string{"1|<<<<<<< HEAD"}, LineRanges: []evidence.LineRange{{Start: 1, End: 1}}, SupersededBy: "read#2"}
	current := evidence.Record{Handle: "read#2", Kind: "read", Shape: evidence.ShapeFileRegion, Path: old.Path, Body: []string{"1|resolved cards"}, LineRanges: old.LineRanges}
	ev := evidence.NamespaceLedger(evidence.AssembleLedger([]evidence.Record{old, current}), "worker")
	if ev.Handles["worker:read#1"].SupersededBy != "worker:read#2" {
		t.Fatal("replacement escaped worker namespace")
	}
	if got := ev.ByPath[old.Path]; len(got) != 1 || got[0] != "worker:read#2" {
		t.Fatalf("unqualified path includes stale observations: %v", got)
	}
	for _, tc := range []struct {
		name, handle, excerpt string
		want                  string
	}{
		{"historical", "worker:read#1", "<<<<<<< HEAD", ""},
		{"current", "worker:read#2", "resolved cards", ""},
		{"paraphrase", "worker:read#1", "the earlier merge-conflict marker", ""},
		{"contradiction", "worker:read#1", "resolved cards", InvestCitationUnverifiableCode},
	} {
		t.Run(tc.name, func(t *testing.T) {
			report := CoordinatorCompletionReport{Synthesis: "File observation.", CitedEvidence: []CoordinatorCitedEvidence{{Evidence: tc.handle, Path: old.Path, Line: 1, Excerpt: tc.excerpt}}}
			eval := EvaluateCloseoutCitations(evidence.CitationRoots{}, "implement_investigate", report, CloseoutEvidence{Ledger: ev})
			if eval.Code != tc.want {
				t.Fatalf("evaluation = %+v want %q", eval, tc.want)
			}
		})
	}
}

func TestRetainedFallbackPinsNarrativeAndNeverAddsUnrelatedCitations(t *testing.T) {
	ev := CloseoutEvidence{Ledger: evidence.AssembleLedger([]evidence.Record{
		{Handle: "command#1", Kind: "command", Shape: evidence.ShapeCommand},
		{Handle: "read#1", Kind: "read", Shape: evidence.ShapeFileRegion, Path: "unrelated.go"},
	})}
	original := CoordinatorCompletionReport{Synthesis: "Original report.", Headline: "Original headline", CitedEvidence: []CoordinatorCitedEvidence{{Evidence: "missing#1"}}}
	first, err := MarshalCoordinatorCompletionReport(original)
	testutil.FailErr(t, "marshal original", err)
	next, err := MarshalCoordinatorCompletionReport(CoordinatorCompletionReport{Synthesis: "Rewritten report.", CitedEvidence: []CoordinatorCitedEvidence{{Evidence: "command#1"}, {Evidence: "missing#2"}}})
	testutil.FailErr(t, "marshal retry", err)
	pinned, ok := ParseCoordinatorCompletionReport(RetainCloseoutDraft(first, next, false))
	if !ok || pinned.Synthesis != original.Synthesis || pinned.Headline != original.Headline {
		t.Fatalf("pinned = %+v", pinned)
	}
	result, grounding := AssembleRetainedCloseout(evidence.CitationRoots{}, "implement_investigate", ev, pinned, InvestHandleNotObservedCode, 3)
	if result.Synthesis != original.Synthesis || len(result.CitedEvidence) != 1 || result.CitedEvidence[0].Evidence != "command#1" {
		t.Fatalf("fallback = %+v", result)
	}
	if grounding.Traced || !grounding.HostAssembled || grounding.RetryCount != 3 || grounding.HintCode != InvestHandleNotObservedCode {
		t.Fatalf("fallback audit = %+v", grounding)
	}
	for _, citation := range grounding.CitedEvidence {
		if strings.Contains(citation.Path, "unrelated") {
			t.Fatal("fallback invented supporting references")
		}
	}
}

// A citation retry cannot rewrite the report; a retry after a document field
// was rejected replaces the document fields and keeps the first narrative.
func TestPinCloseoutReportRepairsOnlyWhatWasRejected(t *testing.T) {
	original := CoordinatorCompletionReport{
		Synthesis: "Original.", Headline: "Original headline",
		Findings: []CoordinatorFinding{{Title: "Original finding"}},
	}
	repaired := CoordinatorCompletionReport{
		Synthesis: "Rewritten.", Headline: "Rewritten headline",
		Findings:      []CoordinatorFinding{{Title: "Original finding", Disposition: "act"}},
		Ask:           &CoordinatorAsk{Do: "Approve.", Effort: "small"},
		CitedEvidence: []CoordinatorCitedEvidence{{Evidence: "read#1"}},
	}
	citation := PinCloseoutReport(original, repaired, false)
	if citation.Headline != "Original headline" || citation.Ask != nil || citation.Findings[0].Disposition != "" || len(citation.CitedEvidence) != 1 {
		t.Fatalf("citation repair = %+v, want only the citations replaced", citation)
	}
	document := PinCloseoutReport(original, repaired, true)
	if document.Synthesis != "Original." || document.Headline != "Rewritten headline" || document.Ask == nil || document.Findings[0].Disposition != "act" {
		t.Fatalf("document repair = %+v, want the document fields replaced under the first narrative", document)
	}
	if !RepairsReportDocument([]string{"SYNTH_HANDLE_NOT_IN_LEGS", ReportInventoryUnaccountedCode}) || RepairsReportDocument([]string{"SYNTH_HANDLE_NOT_IN_LEGS"}) {
		t.Fatal("document repair must follow the rejection codes on the cycle")
	}
}

func TestURLRepairKeepsObservedURLsWithoutMarkingMissingURLsMatched(t *testing.T) {
	const observed = "https://example.com/"
	const missing = "https://citation-fixture.invalid/not-observed"
	ev := CloseoutEvidence{Ledger: evidence.AssembleLedger([]evidence.Record{
		{Handle: "web#1", Kind: "web", Shape: evidence.ShapeURL, SourceTool: "fetch_url", URL: observed},
	})}
	report := CoordinatorCompletionReport{Synthesis: "The page describes a documentation example.", CitedURLs: []string{observed, missing}}
	roots := evidence.CitationRoots{}
	eval := EvaluateCloseoutCitations(roots, "implement_investigate", report, ev)
	if eval.Code != InvestURLNotObservedCode {
		t.Fatalf("URL evaluation = %+v", eval)
	}
	g := BuildCloseoutCitationGrounding(roots, "implement_investigate", report, ev, eval)
	if g == nil || g.Traced {
		t.Fatalf("unobserved URL was traced: %+v", g)
	}
	for _, check := range g.Checks {
		for _, token := range check.Matched {
			if token == missing {
				t.Fatalf("unobserved URL was marked matched: %+v", check)
			}
		}
	}
	data := CloseoutRepairHintData(roots, "implement_investigate", report, ev, eval.Offenders)
	var retained citationRepairTrailer
	testutil.FailErr(t, "decode retained URLs", json.Unmarshal([]byte(data["retained_citations"].(string)), &retained))
	if len(retained.CitedURLs) != 1 || retained.CitedURLs[0] != observed {
		t.Fatalf("retained URLs = %v", retained.CitedURLs)
	}
	fallback, grounding := AssembleRetainedCloseout(roots, "implement_investigate", ev, report, eval.Code, 3)
	if len(fallback.CitedURLs) != 1 || fallback.CitedURLs[0] != observed || grounding.Traced || !grounding.HostAssembled {
		t.Fatalf("URL fallback = %+v, grounding = %+v", fallback, grounding)
	}
}

func TestExhaustedCitationRepairAttachesObservedSourcesWhenNoneRemain(t *testing.T) {
	ev := CloseoutEvidence{Ledger: evidence.AssembleLedger([]evidence.Record{
		{Handle: "read#1", Kind: "read", Shape: evidence.ShapeFileRegion, Path: "observed.go", Body: []string{"package observed"}},
	})}
	draft := CoordinatorCompletionReport{
		Synthesis: "The package was inspected.", Headline: "Package overview",
		CitedEvidence: []CoordinatorCitedEvidence{{Evidence: "read#999"}},
	}
	report, grounding := AssembleRetainedCloseout(evidence.CitationRoots{}, "implement_investigate", ev, draft, InvestHandleNotObservedCode, 3)
	if report.Synthesis != draft.Synthesis || report.Headline != draft.Headline {
		t.Fatalf("fallback changed the answer: %+v", report)
	}
	if grounding == nil || !grounding.HostAssembled || grounding.Traced || grounding.RetryCount != 3 || grounding.HintCode != InvestHandleNotObservedCode {
		t.Fatalf("fallback lost its provenance or repair outcome: %+v", grounding)
	}
	if len(grounding.CitedEvidence) != 1 || grounding.CitedEvidence[0].Handle != "read#1" {
		t.Fatalf("fallback did not expose the recorded source: %+v", grounding.CitedEvidence)
	}
	if len(grounding.Checks) == 0 || grounding.Checks[0].Status != "failed" {
		t.Fatalf("fallback lost the rejected reference check: %+v", grounding.Checks)
	}
}
