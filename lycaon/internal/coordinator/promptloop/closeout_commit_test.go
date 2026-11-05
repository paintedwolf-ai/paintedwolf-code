package promptloop

import (
	"context"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/citationmd"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/jsonshape"
	"github.com/lycaon/lycaon/internal/oar"
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
			st := &promptLoopTurnState{coordinatorFrame: testReportFrame(), draftSlotID: "slot-1", draftSlotAppended: true, turnBatchState: turnBatchState{turnTools: []string{"read"}}}
			loop := NewPromptLoopForTest(PromptLoopDeps{
				Closeout: CloseoutDeps{
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
					AssembleLedgerCloseout: func(_ context.Context, _, _ string, forcedBy []string, drafted guidance.CloseoutDraft, retryCount int) (guidance.CoordinatorCompletionReport, *api.CitationGrounding) {
						if len(forcedBy) != 1 || forcedBy[0] != tc.code {
							t.Fatalf("forcedBy = %v want [%s]", forcedBy, tc.code)
						}
						parsed := drafted.Report()
						if parsed.Synthesis != "no citations here" || parsed.Headline != "Overview" || len(parsed.Limits) != 1 {
							t.Fatalf("drafted = %+v", parsed)
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
				},
				Projection: ProjectionDeps{
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
				},
			})

			history := []api.Message{
				{ID: "u1", Role: api.MessageRoleUser, Content: "research"},
				{ID: "t1", Role: api.MessageRoleTool, EvidenceHandles: []string{"read#1"}},
				{ID: "slot-1", Role: api.MessageRoleAssistant, Content: "no citations here", Visibility: api.MessageVisibilityInternal},
			}
			read := markdownRead("no citations here\n\n```json\n{\"headline\":\"Overview\",\"limits\":[\"Inspection only.\"]}\n```", true)
			out, err := loop.Closeout.handleAcceptedCloseoutReport(
				context.Background(), &api.Session{ID: "s1", WorkspacePath: t.TempDir()},
				"s1", "", tc.surface, st, history,
				api.Message{ID: "slot-1", Role: api.MessageRoleAssistant, Content: "no citations here"},
				read,
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
			if committed.CompletionReport.Headline != "Overview" || len(committed.CompletionReport.Limits) != 1 {
				t.Fatalf("host attachment lost report metadata: %+v", committed.CompletionReport)
			}
			if st.draftSlotID != "" {
				t.Fatalf("draft slot = %q want closed after assemble", st.draftSlotID)
			}
		})
	}
}

// A retry that rewrites the retained answer is refused on the citation budget, and
// what stays retained is the first answer.
func TestCloseoutCitationRetryKeepsPinnedSynthesis(t *testing.T) {
	const pinned = "Honest report: tea install failed."
	retained := activeRetained(pinned, 1, false, guidance.InvestHandleNotObservedCode)
	var noted guidance.CloseoutDraft
	var decisionCode string
	hints := loadCoordinatorTestHintConfig(t)
	st := &promptLoopTurnState{coordinatorFrame: testReportFrame(), draftSlotID: "slot-1", draftSlotAppended: true, turnBatchState: turnBatchState{turnTools: []string{"read"}}, turnCloseoutState: turnCloseoutState{closeoutRetry: closeoutRetryState{attempt: 1}}}
	loop := NewPromptLoopForTest(PromptLoopDeps{
		Closeout: CloseoutDeps{
			HintConfig: hints,
			RejectFmt:  guidance.NewStaticRejectFormatter(hints),
			EvidenceLedger: closeoutLedgerReader{ledger: evidence.Ledger{
				Handles: map[string]evidence.Record{
					"read#1": {Handle: "read#1", Kind: "read", Path: "src/a.go", Body: []string{"package main"}},
				},
				ByPath:       map[string][]string{"src/a.go": {"read#1"}},
				PathFidelity: map[string]string{"src/a.go": "structured"},
			}},
			CloseoutStallState: func(_ context.Context, _ string) guidance.RetainedCloseout { return retained },
			EvaluateCloseoutBlock: func(_ context.Context, _ *api.Session, gc *oar.GuardContext) (*oar.Decision, error) {
				if gc.Rejection.RejectObservation != "citation_repair_invalid" {
					t.Fatalf("observation = %q", gc.Rejection.RejectObservation)
				}
				decisionCode = guidance.CitationRepairInvalidCode
				return &oar.Decision{Code: decisionCode, Data: gc.RejectData[decisionCode]}, nil
			},
			NoteCloseoutGroundingReject: func(_ context.Context, _, _, _ string, draft guidance.CloseoutDraft, _ []jsonshape.Issue) (int, string) {
				noted = draft
				return 2, ""
			},
		},
		Projection: ProjectionDeps{
			AppendMessages:     func(context.Context, string, ...api.Message) error { return nil },
			AppendDraftVersion: func(context.Context, string, string, string, string) (int, error) { return 1, nil },
			UpdateMessage:      func(_ context.Context, _, _ string, _ api.Message) error { return nil },
		},
	})
	const rewrite = "Invented REST CLI success story"
	history := []api.Message{
		{ID: "u1", Role: api.MessageRoleUser, Content: "research"},
		{ID: "slot-1", Role: api.MessageRoleAssistant, Content: rewrite, Visibility: api.MessageVisibilityInternal},
	}
	out, err := loop.Closeout.handleAcceptedCloseoutReport(
		context.Background(), &api.Session{ID: "s1", WorkspacePath: t.TempDir()},
		"s1", "", "implement_investigate", st, history,
		api.Message{ID: "slot-1", Role: api.MessageRoleAssistant, Content: rewrite},
		guidance.ReadCloseoutReport(rewrite, retained, false, citationmd.Options{}),
	)
	testutil.FailErr(t, "handleAcceptedCloseoutReport", err)
	if out.committed || !out.retry || decisionCode == "" {
		t.Fatalf("outcome = %+v", out)
	}
	if noted.Synthesis != pinned || out.drafted.Synthesis != pinned {
		t.Fatalf("retained = %q / %q want pinned %q", noted.Synthesis, out.drafted.Synthesis, pinned)
	}
	if st.closeoutRetry.attempt != 2 || st.closeoutRetry.documentAttempt != 0 {
		t.Fatalf("a refused repair spends the citation budget: %+v", st.closeoutRetry)
	}
}

func TestCloseoutCitationRepairPreservesReportOnCommit(t *testing.T) {
	for _, removeAll := range []bool{false, true} {
		name := "repaired"
		if removeAll {
			name = "references removed"
		}
		t.Run(name, func(t *testing.T) {
			retained := activeRetained("The merge failed [missing#1].\n\n```json\n"+`{"headline":"Conflicts remain","summary":"Repair is needed.","limits":["No commit created."]}`+"\n```", 1, true, guidance.InvestHandleNotObservedCode)
			ledger := evidence.AssembleLedger([]evidence.Record{{Handle: "command#1", Kind: "command", Shape: evidence.ShapeCommand, Body: []string{"merge conflict"}}})
			var committed api.Message
			var assembled bool
			loop := NewPromptLoopForTest(PromptLoopDeps{
				Closeout: CloseoutDeps{
					HintConfig:     loadCoordinatorTestHintConfig(t),
					EvidenceLedger: closeoutLedgerReader{ledger: ledger},
					CloseoutStallState: func(context.Context, string) guidance.RetainedCloseout {
						return retained
					},
					AssembleLedgerCloseout: func(_ context.Context, _, surface string, codes []string, draft guidance.CloseoutDraft, retries int) (guidance.CoordinatorCompletionReport, *api.CitationGrounding) {
						assembled = true
						return guidance.AssembleRetainedCloseout(evidence.CitationRoots{}, surface, guidance.CloseoutEvidence{Ledger: ledger}, draft, nil, codes[0], retries)
					},
					EvaluateCloseoutBlock: func(context.Context, *api.Session, *oar.GuardContext) (*oar.Decision, error) { return nil, nil },
				},
				Projection: ProjectionDeps{
					UpdateMessage: func(_ context.Context, _, _ string, msg api.Message) error {
						committed = msg
						return nil
					},
				},
			})
			cleared := 0
			loop.Closeout.Deps.ClearCloseoutStall = func(context.Context, string) { cleared++ }
			raw := "[cite: c1 => command#1]"
			if removeAll {
				raw = "The merge failed."
			}
			st := &promptLoopTurnState{coordinatorFrame: testReportFrame(), draftSlotID: "slot-1", draftSlotAppended: true, turnBatchState: turnBatchState{turnTools: []string{"command"}}, turnCloseoutState: turnCloseoutState{closeoutRetry: closeoutRetryState{attempt: 1}}}
			out, err := loop.Closeout.handleAcceptedCloseoutReport(t.Context(), &api.Session{ID: "s1", WorkspacePath: t.TempDir()}, "s1", "", "implement_investigate", st,
				[]api.Message{{ID: "u1", Role: api.MessageRoleUser, Content: "merge"}, {ID: "t1", Role: api.MessageRoleTool, EvidenceHandles: []string{"command#1"}}, {ID: "slot-1", Role: api.MessageRoleAssistant}},
				api.Message{ID: "slot-1", Role: api.MessageRoleAssistant}, guidance.ReadCloseoutReport(raw, retained, true, citationmd.Options{}))
			testutil.FailErr(t, "commit citation repair", err)
			if !out.committed || assembled != removeAll {
				t.Fatalf("commit=%v assembled=%v", out.committed, assembled)
			}
			if cleared != 1 || st.closeoutRetry.attempt != 0 || st.closeoutRetry.prevKey != "" || len(st.closeoutRetry.codes) != 0 {
				t.Fatalf("accepted report retained retry state: clears=%d state=%+v", cleared, st.closeoutRetry)
			}
			if committed.Content != "The merge failed." || committed.CompletionReport == nil || committed.CompletionReport.Headline != "Conflicts remain" || committed.CompletionReport.Summary != "Repair is needed." || len(committed.CompletionReport.Limits) != 1 {
				t.Fatalf("report changed during citation repair: %+v", committed)
			}
			if committed.Grounding == nil || committed.Grounding.Traced == removeAll {
				t.Fatalf("repair grounding=%+v", committed.Grounding)
			}
		})
	}
}
