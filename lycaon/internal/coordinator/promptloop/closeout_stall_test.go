package promptloop

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestCitationOnlyRetryStitchesPersistedCloseoutDraftBeforeGuard(t *testing.T) {
	const pinned = "## Recon report\n\nThe backend manages session lifecycle."
	loop := NewPromptLoopForTest(PromptLoopDeps{
		Closeout: CloseoutDeps{
			CloseoutStallState: func(_ context.Context, sessionID string) guidance.RetainedCloseout {
				if sessionID != "session-1" {
					return guidance.RetainedCloseout{}
				}
				return guidance.RetainedCloseout{Active: true, Attempt: 1, PrevKey: guidance.SynthHandleNotInLegsCode, Drafted: pinned, ForcedBy: []string{guidance.SynthHandleNotInLegsCode}}
			},
		},
	})
	const trailer = "```json\n{\"cited_evidence\":[{\"evidence\":\"leg-1:list#1\"}],\"cited_urls\":[],\"artifact_ids\":[]}\n```"
	history := []api.Message{{ID: "draft-1", Role: api.MessageRoleAssistant, Content: trailer}}
	prepared, _ := loop.Tools.maybeCoerceCloseoutContent(
		context.Background(), history, "session-1", "implement_synthesis", trailer, "draft-1",
	)
	report, ok := guidance.ParseCoordinatorCompletionReport(prepared)
	if !ok {
		t.Fatalf("stitched retry did not become a closeout envelope: %q", prepared)
	}
	if report.Synthesis != pinned || len(report.CitedEvidence) != 1 || report.CitedEvidence[0].Evidence != "leg-1:list#1" {
		t.Fatalf("stitched report = %+v", report)
	}
	if history[0].Content != prepared {
		t.Fatalf("history kept unstiched citation fence: %q", history[0].Content)
	}
	inactive := NewPromptLoopForTest(PromptLoopDeps{
		Closeout: CloseoutDeps{
			CloseoutStallState: func(context.Context, string) guidance.RetainedCloseout {
				return guidance.RetainedCloseout{Drafted: pinned}
			},
		},
	})
	if got, _ := inactive.Tools.maybeCoerceCloseoutContent(context.Background(), nil, "session-1", "implement_synthesis", trailer, ""); got != trailer {
		t.Fatalf("inactive stall stitched stale draft: %q", got)
	}

	staleHistory := []api.Message{{Role: api.MessageRoleAssistant, Content: pinned}, {Role: api.MessageRoleUser, Content: "Pinned body: " + pinned}}
	got, _ := inactive.Tools.maybeCoerceCloseoutContent(t.Context(), staleHistory, "session-1", "implement_synthesis", trailer, "")
	if _, ok := guidance.ParseCoordinatorCompletionReport(got); ok {
		t.Fatalf("citation-only repair recovered retired report from history: %s", got)
	}
}

func TestCloseoutToolRecoveryRetiresBeforeEveryOutcome(t *testing.T) {
	for _, outcome := range []string{"completed", "failed", "rejected", "blocked", "checkpoint failure"} {
		t.Run(outcome, func(t *testing.T) {
			retained := guidance.RetainedCloseout{Active: true, Attempt: 2, DocumentAttempt: 1, Drafted: "stale success", ForcedBy: []string{"citation"}}
			reg := tools.NewStubRegistry()
			executed := 0
			defer func() {
				want := 0
				if outcome == "completed" || outcome == "failed" {
					want = 7
				}
				if executed != want {
					t.Fatalf("handler executions=%d, want %d", executed, want)
				}
			}()
			_ = reg.Register("read", func(context.Context, map[string]any, tools.ToolContext) (string, error) {
				executed++
				if retained.Active {
					t.Fatal("tool ran before pinned report retired")
				}
				if outcome == "failed" {
					return "", errors.New("read failed")
				}
				return "current observation", nil
			})
			loop := NewPromptLoopForTest(PromptLoopDeps{Context: ContextDeps{Tools: reg}, Closeout: CloseoutDeps{
				CloseoutStallState: func(context.Context, string) guidance.RetainedCloseout { return retained },
				ResumeCloseoutWork: func(context.Context, string) {
					retained = guidance.RetainedCloseout{Attempt: retained.Attempt, DocumentAttempt: retained.DocumentAttempt}
				},
				AssembleLedgerCloseout: func(context.Context, string, string, []string, string, int) (guidance.CoordinatorCompletionReport, *api.CitationGrounding) {
					t.Fatal("tool recovery assembled an old conclusion")
					return guidance.CoordinatorCompletionReport{}, nil
				},
			}, Tools: ToolsDeps{BeforeToolRun: func(context.Context, *api.Session, []api.Message, string, string, map[string]any) (string, bool, error) {
				if retained.Active {
					t.Fatal("admission saw active pin")
				}
				if outcome == "rejected" || outcome == "blocked" {
					return "", false, guidance.NewRefusal("COMMAND_REPEAT_FAILURE", "failed prior observation")
				}
				return "", false, nil
			}}, Projection: ProjectionDeps{AppendMessages: func(context.Context, string, ...api.Message) error { return nil }, CheckpointTurn: func(context.Context, string, string, store.TurnPhase, string) error {
				if retained.Active {
					t.Fatal("checkpoint saw active pin")
				}
				if outcome == "checkpoint failure" {
					return errors.New("checkpoint failed")
				}
				return nil
			}}})
			st := &promptLoopTurnState{turnID: "turn", attemptID: "attempt", closeoutRetry: closeoutRetryState{attempt: 2, documentAttempt: 1, prevKey: "old", codes: []string{"citation"}}}
			sess := &api.Session{ID: "session"}
			for i := 0; i < 7; i++ {
				call := api.ToolCall{ID: fmt.Sprint(i), Name: "read", Args: map[string]any{"path": "missing"}}
				msg := api.Message{ID: fmt.Sprint("assistant", i), Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{call}}
				st.history = append(st.history, msg)
				result, err := loop.processPromptLoopAssistantTurn(t.Context(), sess, sess.ID, PromptRunInput{}, i, 20, "implement_investigate", "inspect", st, msg, &modelcall.Completion{ToolCalls: msg.ToolCalls})
				if outcome == "checkpoint failure" {
					if err == nil {
						t.Fatal("expected checkpoint failure")
					}
				} else {
					testutil.FailErr(t, "process recovery attempt", err)
				}
				if retained.Active || retained.Drafted != "" || st.closeoutRetry.attempt != 2 || st.closeoutRetry.documentAttempt != 1 || st.closeoutRetry.prevKey != "" || len(st.closeoutRetry.codes) != 0 {
					t.Fatalf("retirement lost invariant: %+v / %+v", retained, st.closeoutRetry)
				}
				if result.exit == loopExitCompleted {
					t.Fatal("tool recovery accepted stale report")
				}
				if result.breakLoop {
					if outcome == "blocked" || outcome == "rejected" {
						if result.exit != loopExitBlockedLoop {
							t.Fatalf("repeated refusal exit = %v", result.exit)
						}
						return
					}
					break
				}
			}
			if outcome == "blocked" {
				t.Fatal("repeated rejected recovery did not stop")
			}
			fresh := "Fresh conclusion: the file is still missing."
			got, _ := loop.Tools.maybeCoerceCloseoutContent(t.Context(), nil, sess.ID, "implement_investigate", fresh, "")
			report, ok := guidance.ParseCoordinatorCompletionReport(got)
			if !ok || report.Synthesis != fresh || loop.Closeout.pinnedCloseoutSynthesis(t.Context(), sess.ID) != "" {
				t.Fatalf("fresh report overwritten: %q", got)
			}
		})
	}
}

func TestCloseoutRecoveryCopyUsesCallableTools(t *testing.T) {
	reg, err := anchor.LoadRegistryFromConfigRoot()
	testutil.FailErr(t, "load anchor registry", err)
	release := anchor.SetAnchorsFor(func(context.Context, string) *anchor.Registry { return reg })
	defer release()

	for _, tc := range []struct {
		name                  string
		offered               []string
		prose, document, want bool
	}{
		{name: "callable", offered: []string{"read"}, want: true},
		{name: "unstamped"}, {name: "empty", offered: []string{}},
		{name: "prose only", offered: []string{"read"}, prose: true},
		{name: "document repair", offered: []string{"read"}, document: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
			var rendered string
			loop := NewPromptLoopForTest(PromptLoopDeps{Projection: ProjectionDeps{AppendMessages: func(context.Context, string, ...api.Message) error { return nil }}, Closeout: CloseoutDeps{
				HintConfig: loadCoordinatorTestHintConfig(t),
				CloseoutStallState: func(context.Context, string) guidance.RetainedCloseout {
					if tc.document {
						return guidance.RetainedCloseout{Active: true, ForcedBy: []string{guidance.ReportDocumentInvalidCode}}
					}
					return guidance.RetainedCloseout{}
				},
				RenderHostKick: func(ctx context.Context, id string, data map[string]any) (string, error) {
					var err error
					rendered, err = engine.RenderKick(ctx, id, data)
					return rendered, err
				},
			}})
			st := &promptLoopTurnState{proseFinish: tc.prose, offeredToolNames: tc.offered}
			_, err := loop.Closeout.rejectCloseoutGrounding(t.Context(), "session", st, nil, "", &oar.Decision{Code: guidance.InvestHandleNotObservedCode, Copy: map[string]string{"what": "Citation is unobserved"}}, "Pinned report", nil)
			testutil.FailErr(t, "render closeout rejection", err)
			if got := strings.Contains(rendered, "use an offered tool"); got != tc.want {
				t.Fatalf("offered guidance = %v, want %v: %s", got, tc.want, rendered)
			}
			if !strings.Contains(rendered, "Pinned body:") || !strings.Contains(rendered, "emit only the citations fence") {
				t.Fatalf("metadata repair contract missing: %s", rendered)
			}
		})
	}
}

func TestCloseoutAsyncRetirementClearsLocalOffenderBeforeFreshReport(t *testing.T) {
	const fresh = "Fresh report after worker failed."
	stopped := errors.New("inspection boundary")
	st := &promptLoopTurnState{closeoutRetry: closeoutRetryState{attempt: 2, documentAttempt: 1, prevKey: "old offender", codes: []string{"old refusal"}}}
	loop := NewPromptLoopForTest(PromptLoopDeps{Closeout: CloseoutDeps{
		CloseoutStallState: func(context.Context, string) guidance.RetainedCloseout {
			return guidance.RetainedCloseout{Attempt: 2, DocumentAttempt: 1}
		},
		EvaluateCloseoutBlock: func(context.Context, *api.Session, *oar.GuardContext) (*oar.Decision, error) {
			if st.closeoutRetry.prevKey != "" || len(st.closeoutRetry.codes) != 0 || st.closeoutRetry.attempt != 2 || st.closeoutRetry.documentAttempt != 1 {
				t.Fatalf("stale local repair state: %+v", st.closeoutRetry)
			}
			return nil, stopped
		},
	}})
	_, err := loop.Closeout.handleAcceptedCloseoutReport(t.Context(), &api.Session{ID: "session"}, "session", "", "implement_investigate", st, nil, api.Message{}, guidance.CloseoutRead{Report: guidance.CoordinatorCompletionReport{Synthesis: fresh}})
	if !errors.Is(err, stopped) {
		t.Fatalf("fresh report didn't reach observation boundary: %v", err)
	}
}
