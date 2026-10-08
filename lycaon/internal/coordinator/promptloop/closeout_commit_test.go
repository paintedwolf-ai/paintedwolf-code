package promptloop

import (
	"context"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type closeoutLedgerReader struct{ ledger evidence.Ledger }

func (r closeoutLedgerReader) LoadLedger(_ context.Context, _ string) (evidence.Ledger, error) {
	return r.ledger, nil
}

func (closeoutLedgerReader) WorkerLegs(context.Context, string, time.Time) ([]guidance.EvidenceLeg, error) {
	return nil, nil
}

func TestCloseoutCitationsRequiredHostAssemblesImmediately(t *testing.T) {
	for _, tc := range []struct{ surface, code string }{
		{"implement_investigate", guidance.InvestCitationsRequiredCode},
		{"implement_synthesis", guidance.SynthCitationsRequiredCode},
	} {
		t.Run(tc.surface, func(t *testing.T) {
			var committed api.Message
			var kickAppends int
			st := &promptLoopTurnState{coordinatorFrame: testReportFrame(), draftSlotID: "slot-1", draftSlotAppended: true, turnTools: []string{"read"}}
			loop := NewPromptLoopForTest(PromptLoopDeps{
				HintConfig: loadCoordinatorTestHintConfig(t),
				EvidenceLedger: closeoutLedgerReader{ledger: evidence.Ledger{
					Handles: map[string]evidence.Record{
						"read#1": {
							Handle: "read#1",
							Kind:   "read",
							Path:   "src/a.go",
							Body:   []string{"package main"},
						},
					},
					ByPath:       map[string][]string{"src/a.go": {"read#1"}},
					PathFidelity: map[string]string{"src/a.go": "structured"},
				}},
				AssembleLedgerCloseout: func(_ context.Context, _, _ string, forcedBy []string, drafted string, retryCount int) (guidance.CoordinatorCompletionReport, *api.CitationGrounding) {
					if len(forcedBy) != 1 || forcedBy[0] != tc.code {
						t.Fatalf("forcedBy = %v want [%s]", forcedBy, tc.code)
					}
					parsed, ok := guidance.ParseCoordinatorCompletionReport(drafted)
					if !ok || parsed.Synthesis != "no citations here" || parsed.Headline != "Overview" || len(parsed.Limits) != 1 {
						t.Fatalf("drafted = %q", drafted)
					}
					if retryCount != 0 {
						t.Fatalf("retryCount = %d want 0 (no model bounce)", retryCount)
					}
					return parsed,
						&api.CitationGrounding{
							HostAssembled: true,
							Traced:        false,
							HintCode:      tc.code,
							CitedEvidence: []api.CitationGroundingCitedEvidence{{Handle: "read#1", Path: "src/a.go", Verdict: api.CitationVerdictMatched}},
						}
				},
				UpdateMessage: func(_ context.Context, _, _ string, msg api.Message) error {
					committed = msg
					return nil
				},
				AppendMessages: func(_ context.Context, _ string, msgs ...api.Message) error {
					for _, m := range msgs {
						if m.Role == api.MessageRoleUser {
							kickAppends++
						}
					}
					return nil
				},
			})

			history := []api.Message{
				{ID: "u1", Role: api.MessageRoleUser, Content: "research"},
				{ID: "slot-1", Role: api.MessageRoleAssistant, Content: "no citations here", Visibility: api.MessageVisibilityInternal},
			}
			report := guidance.CoordinatorCompletionReport{Synthesis: "no citations here", Headline: "Overview", Limits: []string{"Inspection only."}}
			out, err := turnCloseout{loop}.handleAcceptedCloseoutReport(
				context.Background(), &api.Session{ID: "s1", WorkspacePath: t.TempDir()},
				"s1", "", tc.surface, st, history,
				api.Message{ID: "slot-1", Role: api.MessageRoleAssistant, Content: "no citations here"},
				guidance.CloseoutRead{Report: report},
			)
			testutil.FailErr(t, "handleAcceptedCloseoutReport", err)
			if !out.committed {
				t.Fatal("expected committed=true via host observed-autobind")
			}
			if out.retry || out.exhausted {
				t.Fatalf("expected neither retry nor exhausted, got retry=%v exhausted=%v", out.retry, out.exhausted)
			}
			if kickAppends != 0 {
				t.Fatalf("citation-grounding kick appends = %d want 0", kickAppends)
			}
			if committed.Grounding == nil || !committed.Grounding.HostAssembled {
				t.Fatalf("committed grounding = %+v want host_assembled", committed.Grounding)
			}
			if committed.Grounding.Traced {
				t.Fatal("observed-autobind must stay traced=false")
			}
			if committed.Visibility != api.MessageVisibilityTranscript {
				t.Fatalf("visibility = %q want transcript", committed.Visibility)
			}
			if committed.Kind != api.MessageKindCompletionReport {
				t.Fatalf("kind = %q want completion_report", committed.Kind)
			}
			// Document metadata survives citation repair in an enabled report phase.
			if committed.CompletionReport == nil ||
				committed.CompletionReport.Scope != api.CompletionReportScopeRun {
				t.Fatalf("completion report meta = %+v want run scope", committed.CompletionReport)
			}
			if committed.CompletionReport.Headline != report.Headline || len(committed.CompletionReport.Limits) != 1 {
				t.Fatalf("host attachment lost report metadata: %+v", committed.CompletionReport)
			}
			if st.draftSlotID != "" {
				t.Fatalf("draft slot = %q want closed after assemble", st.draftSlotID)
			}
		})
	}
}

func TestCloseoutCitationRetryKeepsPinnedSynthesis(t *testing.T) {
	const pinned = "Honest report: tea install failed."
	var assembled string
	st := &promptLoopTurnState{coordinatorFrame: testReportFrame(), draftSlotID: "slot-1", draftSlotAppended: true, turnTools: []string{"read"}}
	loop := NewPromptLoopForTest(PromptLoopDeps{
		HintConfig: loadCoordinatorTestHintConfig(t),
		EvidenceLedger: closeoutLedgerReader{ledger: evidence.Ledger{
			Handles: map[string]evidence.Record{
				"read#1": {Handle: "read#1", Kind: "read", Path: "src/a.go", Body: []string{"package main"}},
			},
			ByPath:       map[string][]string{"src/a.go": {"read#1"}},
			PathFidelity: map[string]string{"src/a.go": "structured"},
		}},
		CloseoutStallState: func(_ context.Context, _ string) guidance.RetainedCloseout {
			return guidance.RetainedCloseout{Active: true, Attempt: 1, PrevKey: "k", Drafted: pinned}
		},
		AssembleLedgerCloseout: func(_ context.Context, _, _ string, _ []string, drafted string, _ int) (guidance.CoordinatorCompletionReport, *api.CitationGrounding) {
			assembled = drafted
			return guidance.CoordinatorCompletionReport{Synthesis: drafted},
				&api.CitationGrounding{HostAssembled: true, HintCode: guidance.InvestCitationsRequiredCode}
		},
		UpdateMessage: func(_ context.Context, _, _ string, _ api.Message) error { return nil },
	})
	history := []api.Message{
		{ID: "u1", Role: api.MessageRoleUser, Content: "research"},
		{ID: "slot-1", Role: api.MessageRoleAssistant, Content: "Invented REST CLI success story", Visibility: api.MessageVisibilityInternal},
	}
	out, err := turnCloseout{loop}.handleAcceptedCloseoutReport(
		context.Background(), &api.Session{ID: "s1", WorkspacePath: t.TempDir()},
		"s1", "", "implement_investigate", st, history,
		api.Message{ID: "slot-1", Role: api.MessageRoleAssistant, Content: "Invented REST CLI success story"},
		guidance.CloseoutRead{Report: guidance.CoordinatorCompletionReport{Synthesis: "Invented REST CLI success story"}},
	)
	testutil.FailErr(t, "handleAcceptedCloseoutReport", err)
	if !out.committed {
		t.Fatal("expected host assemble of the pinned body")
	}
	if guidance.UsableCloseoutSynthesis(assembled) != pinned {
		t.Fatalf("assembled drafted = %q want pinned %q", assembled, pinned)
	}
}

func TestCloseoutCitationRepairPreservesReportOnCommit(t *testing.T) {
	for _, removeAll := range []bool{false, true} {
		name := "repaired"
		if removeAll {
			name = "references removed"
		}
		t.Run(name, func(t *testing.T) {
			original := guidance.CoordinatorCompletionReport{
				Synthesis: "The merge failed.", Headline: "Conflicts remain", Summary: "Repair is needed.",
				Limits: []string{"No commit created."}, CitedEvidence: []guidance.CoordinatorCitedEvidence{{Evidence: "missing#1"}},
			}
			draft, err := guidance.MarshalCoordinatorCompletionReport(original)
			testutil.FailErr(t, "marshal retained report", err)
			ledger := evidence.AssembleLedger([]evidence.Record{{Handle: "command#1", Kind: "command", Shape: evidence.ShapeCommand, Body: []string{"merge conflict"}}})
			var committed api.Message
			var assembled bool
			loop := NewPromptLoopForTest(PromptLoopDeps{
				HintConfig: loadCoordinatorTestHintConfig(t), EvidenceLedger: closeoutLedgerReader{ledger: ledger},
				ProseCitationGrounding: func(_ context.Context, _ *api.Session, _ []api.Message, _, raw, surface string) *api.CitationGrounding {
					report, ok := guidance.ParseCoordinatorCompletionReport(raw)
					if !ok {
						t.Fatalf("commit lost the report envelope: %q", raw)
					}
					ev := guidance.CloseoutEvidence{Ledger: ledger}
					eval := guidance.EvaluateCloseoutCitations(evidence.CitationRoots{}, surface, report, ev)
					return guidance.BuildCloseoutCitationGrounding(evidence.CitationRoots{}, surface, report, ev, eval)
				},
				CloseoutStallState: func(context.Context, string) guidance.RetainedCloseout {
					return guidance.RetainedCloseout{Active: true, Attempt: 1, PrevKey: guidance.InvestHandleNotObservedCode, Drafted: draft}
				},
				AssembleLedgerCloseout: func(_ context.Context, _, surface string, codes []string, raw string, retries int) (guidance.CoordinatorCompletionReport, *api.CitationGrounding) {
					assembled = true
					report, ok := guidance.ParseCoordinatorCompletionReport(raw)
					if !ok {
						t.Fatalf("fallback lost the retained report: %q", raw)
					}
					return guidance.AssembleRetainedCloseout(evidence.CitationRoots{}, surface, guidance.CloseoutEvidence{Ledger: ledger}, report, codes[0], retries)
				},
				UpdateMessage: func(_ context.Context, _, _ string, msg api.Message) error {
					committed = msg
					return nil
				},
			})
			cleared := 0
			loop.Deps.ClearCloseoutStall = func(context.Context, string) { cleared++ }
			repaired := guidance.CoordinatorCompletionReport{Synthesis: "The merge succeeded.", Headline: "All done"}
			if !removeAll {
				repaired.CitedEvidence = []guidance.CoordinatorCitedEvidence{{Evidence: "command#1"}}
			}
			st := &promptLoopTurnState{coordinatorFrame: testReportFrame(), draftSlotID: "slot-1", draftSlotAppended: true, turnTools: []string{"command"}, closeoutRetry: closeoutRetryState{attempt: 1}}
			out, err := turnCloseout{loop}.handleAcceptedCloseoutReport(t.Context(), &api.Session{ID: "s1", WorkspacePath: t.TempDir()}, "s1", "", "implement_investigate", st,
				[]api.Message{{ID: "u1", Role: api.MessageRoleUser, Content: "merge"}, {ID: "slot-1", Role: api.MessageRoleAssistant}},
				api.Message{ID: "slot-1", Role: api.MessageRoleAssistant}, guidance.CloseoutRead{Report: repaired})
			testutil.FailErr(t, "commit citation repair", err)
			if !out.committed || assembled != removeAll {
				t.Fatalf("commit=%v assembled=%v", out.committed, assembled)
			}
			if cleared != 1 || st.closeoutRetry.attempt != 0 || st.closeoutRetry.prevKey != "" || len(st.closeoutRetry.codes) != 0 {
				t.Fatalf("accepted report retained retry state: clears=%d state=%+v", cleared, st.closeoutRetry)
			}
			if committed.Content != original.Synthesis || committed.CompletionReport == nil || committed.CompletionReport.Headline != original.Headline || committed.CompletionReport.Summary != original.Summary || len(committed.CompletionReport.Limits) != 1 {
				t.Fatalf("report changed during citation repair: %+v", committed)
			}
			if committed.Grounding == nil || committed.Grounding.Traced == removeAll {
				t.Fatalf("repair grounding=%+v", committed.Grounding)
			}
		})
	}
}
