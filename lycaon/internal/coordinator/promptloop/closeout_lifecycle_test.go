package promptloop

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestAssembledCloseoutClearsRetryStateOnlyAfterCommit(t *testing.T) {
	for _, result := range []string{"committed", "empty", "failed"} {
		t.Run(result, func(t *testing.T) {
			cleared := 0
			failure := errors.New("commit failed")
			loop := NewPromptLoopForTest(PromptLoopDeps{
				Closeout: CloseoutDeps{
					ClearCloseoutStall: func(context.Context, string) { cleared++ },
					AssembleLedgerCloseout: func(context.Context, string, string, []string, string, int) (guidance.CoordinatorCompletionReport, *api.CitationGrounding) {
						text := "retained report"
						if result == "empty" {
							text = ""
						}
						return guidance.CoordinatorCompletionReport{Synthesis: text}, &api.CitationGrounding{HostAssembled: true}
					},
				},
				Projection: ProjectionDeps{
					UpdateMessage: func(context.Context, string, string, api.Message) error {
						if result == "failed" {
							return failure
						}
						return nil
					},
				},
			})
			st := &promptLoopTurnState{draftSlotID: "draft", draftSlotAppended: true,
				closeoutRetry: closeoutRetryState{attempt: 3, prevKey: "offender", codes: []string{"citation"}}}
			out, err := loop.Closeout.emitAssembledCloseout(t.Context(), &api.Session{ID: "session"}, "session", "", "implement_investigate", "draft", nil, st, nil)
			if result == "failed" {
				if !errors.Is(err, failure) {
					t.Fatalf("error = %v, want commit failure", err)
				}
			} else {
				testutil.FailErr(t, "assemble closeout", err)
			}
			if result == "committed" {
				if !out.committed || cleared != 1 || st.closeoutRetry.attempt != 0 || st.closeoutRetry.prevKey != "" || len(st.closeoutRetry.codes) != 0 {
					t.Fatalf("committed retry state not cleared: cleared=%d state=%+v", cleared, st)
				}
			} else if out.committed || cleared != 0 || st.closeoutRetry.attempt != 3 || st.closeoutRetry.prevKey != "offender" || len(st.closeoutRetry.codes) != 1 {
				t.Fatalf("uncommitted retry state changed: cleared=%d state=%+v", cleared, st)
			}
		})
	}
}

func TestUserSendStartsCloseoutIntentOnlyOnDelivery(t *testing.T) {
	for _, delivery := range []string{"delivered", "empty", "failed"} {
		t.Run(delivery, func(t *testing.T) {
			resets := 0
			failure := errors.New("delivery failed")
			loop := NewPromptLoopForTest(PromptLoopDeps{
				Closeout: CloseoutDeps{
					BeginCloseoutIntent: func(context.Context, string) { resets++ },
				},
				Inbox: InboxDeps{
					TakeUserSend: func(context.Context, string) ([]api.Message, error) {
						switch delivery {
						case "failed":
							return nil, failure
						case "empty":
							return nil, nil
						default:
							return []api.Message{{Role: api.MessageRoleUser, Content: "new direction"}}, nil
						}
					},
				},
			})
			st := &promptLoopTurnState{closeoutRetry: closeoutRetryState{attempt: 3, prevKey: "offender", codes: []string{"citation"}}}
			prompt := "original"
			sent, err := loop.Inbox.takeUserSend(t.Context(), "session", st, &prompt)
			if delivery == "failed" {
				if !errors.Is(err, failure) {
					t.Fatalf("delivery error = %v", err)
				}
			} else {
				testutil.FailErr(t, "deliver user direction", err)
			}
			if delivery == "delivered" {
				if !sent || resets != 1 || st.closeoutRetry.attempt != 0 || st.closeoutRetry.prevKey != "" || len(st.closeoutRetry.codes) != 0 {
					t.Fatalf("new intent retained retry state: resets=%d state=%+v", resets, st)
				}
			} else if sent || resets != 0 || st.closeoutRetry.attempt != 3 || st.closeoutRetry.prevKey != "offender" || len(st.closeoutRetry.codes) != 1 {
				t.Fatalf("no direction changed retry state: resets=%d state=%+v", resets, st)
			}
		})
	}
}

func TestCloseoutContinuationRestoresWholeRetryProjection(t *testing.T) {
	loop := NewPromptLoopForTest(PromptLoopDeps{
		Closeout: CloseoutDeps{
			CloseoutStallState: func(context.Context, string) guidance.RetainedCloseout {
				return guidance.RetainedCloseout{Active: true, Attempt: 3, PrevKey: "offender", Drafted: "retained draft", ForcedBy: []string{"first", "second"}}
			},
		},
	})
	setup, err := loop.preparePromptLoop(t.Context(), PromptRunInput{
		SessionID: "session", Session: &api.Session{ID: "session"}, ProfileID: "coordinator",
	})
	testutil.FailErr(t, "prepare continued closeout", err)
	got := setup.state.closeoutRetry
	if got.attempt != 3 || got.prevKey != "offender" || len(got.codes) != 2 || got.codes[0] != "first" || got.codes[1] != "second" {
		t.Fatalf("incomplete retry projection: %+v", got)
	}
}
