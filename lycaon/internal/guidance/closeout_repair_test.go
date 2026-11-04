package guidance

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/testutil"
)

func repairDraft(t *testing.T, markdown string, ev CloseoutEvidence) (CloseoutDraft, DraftCitationResolution) {
	t.Helper()
	draft := ParseCloseoutDraft(markdown, DraftParseOptions(evidence.CitationRoots{}, ev.Ledger))
	return draft, ResolveDraftCitations(evidence.CitationRoots{}, "implement_investigate", draft, ev, nil)
}

func TestCitationRepairNamesRejectedOccurrencesAndOffersExactToolReceipts(t *testing.T) {
	ev := CloseoutEvidence{Ledger: evidence.AssembleLedger([]evidence.Record{
		{Handle: "command#37", Kind: "command", Shape: evidence.ShapeCommand, SourceTool: "command"},
		{Handle: "git_receipt#1", Kind: "git_receipt", Shape: evidence.ShapeCommand, SourceTool: "git_commit", Path: "data/cards.yaml"},
		{Handle: "read#1", Kind: "read", Shape: evidence.ShapeFileRegion, Path: "unrelated.go"},
	})}
	draft, resolved := repairDraft(t, "The merge is committed [command#37] [git_commit#7].", ev)
	data := CloseoutRepairHintData(evidence.CitationRoots{}, draft, resolved, ev, []string{"git_commit#7"})
	targets := data["repair_targets"].([]map[string]any)
	if len(targets) != 1 || targets[0]["id"] != "c2" || targets[0]["token"] != "git_commit#7" {
		t.Fatalf("targets = %+v, want only the rejected occurrence", targets)
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

func TestRetainedFallbackKeepsResolvableCitationsAndNeverAddsUnrelatedOnes(t *testing.T) {
	ev := CloseoutEvidence{Ledger: evidence.AssembleLedger([]evidence.Record{
		{Handle: "command#1", Kind: "command", Shape: evidence.ShapeCommand},
		{Handle: "read#1", Kind: "read", Shape: evidence.ShapeFileRegion, Path: "unrelated.go"},
	})}
	draft, _ := repairDraft(t, "Original report [command#1] [missing#2].", ev)
	result, grounding := AssembleRetainedCloseout(evidence.CitationRoots{}, "implement_investigate", ev, draft, nil, InvestHandleNotObservedCode, 3)
	if result.Synthesis != "Original report." || len(result.CitedEvidence) != 1 || result.CitedEvidence[0].Evidence != "command#1" {
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

func TestRepairsReportDocumentFollowsTheCycleCodes(t *testing.T) {
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
	roots := evidence.CitationRoots{}
	draft, resolved := repairDraft(t, "The [docs]("+observed+") and the [spec]("+missing+").", ev)
	eval := EvaluateDraftCitations(draft, resolved)
	if eval.Code != InvestURLNotObservedCode || len(eval.UnobservedURLs) != 1 || eval.UnobservedURLs[0] != missing {
		t.Fatalf("URL evaluation = %+v", eval)
	}
	g := BuildCloseoutCitationGrounding(roots, "implement_investigate", draft.Report(), ev, eval)
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
	targets := CloseoutRepairHintData(roots, draft, resolved, ev, eval.Offenders)["repair_targets"].([]map[string]any)
	if len(targets) != 1 || targets[0]["id"] != "c2" || targets[0]["link"] != true {
		t.Fatalf("targets = %+v", targets)
	}
	fallback, grounding := AssembleRetainedCloseout(roots, "implement_investigate", ev, draft, nil, eval.Code, 3)
	if len(fallback.CitedURLs) != 1 || fallback.CitedURLs[0] != observed || grounding.Traced || !grounding.HostAssembled {
		t.Fatalf("URL fallback = %+v, grounding = %+v", fallback, grounding)
	}
	// AC-CLO-11: the unobserved link keeps its label, not its destination.
	if strings.Contains(fallback.Synthesis, missing) || !strings.Contains(fallback.Synthesis, "the spec.") {
		t.Fatalf("fallback display = %q", fallback.Synthesis)
	}
	if !strings.Contains(fallback.Synthesis, "[docs]("+observed+")") {
		t.Fatalf("observed link lost: %q", fallback.Synthesis)
	}
}

func TestExhaustedCitationRepairAttachesObservedSourcesWhenNoneRemain(t *testing.T) {
	ev := CloseoutEvidence{Ledger: evidence.AssembleLedger([]evidence.Record{
		{Handle: "read#1", Kind: "read", Shape: evidence.ShapeFileRegion, Path: "observed.go", Body: []string{"package observed"}},
	})}
	draft, _ := repairDraft(t, "The package was inspected [read#999].", ev)
	draft.setDocument(&WorkflowRunReportDocument{Headline: "Package overview"})
	report, grounding := AssembleRetainedCloseout(evidence.CitationRoots{}, "implement_investigate", ev, draft, nil, InvestHandleNotObservedCode, 3)
	if report.Synthesis != "The package was inspected." || report.Headline != "Package overview" {
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

func TestCitationRepairUnknownReferenceOffersBoundedCurrentChoices(t *testing.T) {
	for count := 0; count <= citationRepairSampleCap+2; count++ {
		for _, namespace := range []string{"", "worker"} {
			t.Run(fmt.Sprintf("%d/%s", count, namespace), func(t *testing.T) {
				records := []evidence.Record{{Handle: "read#1", Kind: "read", Path: "old.go", SupersededBy: "read#2"}}
				for i := 0; i < count; i++ {
					records = append(records, evidence.Record{Handle: fmt.Sprintf("read#%d", i+2), Kind: "read", SourceTool: "read", Path: fmt.Sprintf("file%d.go", i)})
				}
				records = append(records, evidence.Record{Handle: "gate#1", GateType: "review"})
				ledger := evidence.AssembleLedger(records)
				if namespace != "" {
					ledger = evidence.NamespaceLedger(ledger, namespace)
				}
				before, err := json.Marshal(ledger)
				testutil.FailErr(t, "snapshot ledger", err)
				rejected := []CoordinatorCitedEvidence{{Evidence: "file.go:4"}, {Evidence: "unknown#1"}}
				choices := citationRepairObservations(evidence.CitationRoots{}, ledger, rejected)
				want := min(count, citationRepairSampleCap)
				if len(choices) != want {
					t.Fatalf("choices = %+v, want %d", choices, want)
				}
				seen := map[string]bool{}
				for i, choice := range choices {
					wantHandle := fmt.Sprintf("read#%d", count+1-i)
					if namespace != "" {
						wantHandle = namespace + ":" + wantHandle
					}
					if choice.Evidence != wantHandle {
						t.Fatalf("choice %d = %q, want newest current observation %q", i, choice.Evidence, wantHandle)
					}
					rec, ok := evidence.ResolveHandle(ledger, choice.Evidence)
					if !ok || rec.IsGate() || rec.SupersededBy != "" || seen[choice.Evidence] || choice.Path != rec.Path || choice.Tool != rec.SourceTool {
						t.Fatalf("invalid choice: %+v, record: %+v", choice, rec)
					}
					seen[choice.Evidence] = true
				}
				if again := citationRepairObservations(evidence.CitationRoots{}, ledger, rejected); !reflect.DeepEqual(choices, again) {
					t.Fatalf("unstable choices: %+v then %+v", choices, again)
				}
				if choices := citationRepairObservations(evidence.CitationRoots{}, ledger, nil); len(choices) != 0 {
					t.Fatalf("offered choices without a rejected citation: %+v", choices)
				}
				after, err := json.Marshal(ledger)
				testutil.FailErr(t, "snapshot ledger after choices", err)
				if string(before) != string(after) {
					t.Fatal("repair choices mutated the evidence ledger")
				}
			})
		}
	}
}

// Decision 006: a coordinate needs line facts, so the choices say which records have
// them, and the bare handle stays usable on the rest.
func TestCitationRepairChoicesShowUsableCoordinates(t *testing.T) {
	ledger := evidence.AssembleLedger([]evidence.Record{
		{Handle: "read#1", Kind: "read", Shape: evidence.ShapeFileRegion, SourceTool: "read", Path: "a.go", LineRanges: []evidence.LineRange{{Start: 1, End: 5}, {Start: 9, End: 9}}},
		{Handle: "command#1", Kind: "command", Shape: evidence.ShapeCommand, SourceTool: "command"},
	})
	choices := citationRepairObservations(evidence.CitationRoots{}, ledger, []CoordinatorCitedEvidence{{Evidence: "missing#1"}})
	lines := map[string]string{}
	for _, choice := range choices {
		lines[choice.Evidence] = choice.Lines
	}
	if lines["read#1"] != "1-5,9-9" {
		t.Fatalf("read#1 lines = %q", lines["read#1"])
	}
	if got, ok := lines["command#1"]; !ok || got != "" {
		t.Fatalf("command#1 = %q present=%v, want a choice with no coordinate", got, ok)
	}
}
