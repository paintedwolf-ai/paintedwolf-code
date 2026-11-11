package promptloop

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/toolreach"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestNoToolTurnAndCompletionBarrierCarryFrozenCallReach(t *testing.T) {
	var seenNoTool, seenCompletion toolreach.Request
	store := &inPlaceStore{}
	draft := api.Message{ID: "draft", Role: api.MessageRoleAssistant, Content: "All done.", DraftStatus: api.DraftStatusLive, Visibility: api.MessageVisibilityInternal}
	store.Append(draft)
	loop := NewPromptLoopForTest(PromptLoopDeps{
		Closeout: CloseoutDeps{
			CloseoutTrackingDeps: CloseoutTrackingDeps{
				BeforeFinishNoToolTurn: func(ctx context.Context, _ *api.Session, _ []api.Message, _, _ string, _ guidance.CloseoutDraft, _ string, _ bool, _ []string, _ bool) (*guidance.Refusal, bool) {
					seenNoTool = tools.CallReach(ctx)
					return nil, false
				},
			},
			AssertCompletion: func(ctx context.Context, _ *api.Session) error {
				seenCompletion = tools.CallReach(ctx)
				return nil
			},
			AssembleLedgerCloseout: func(context.Context, string, string, []string, guidance.CloseoutDraft, int) (guidance.CoordinatorCompletionReport, *api.CitationGrounding) {
				return guidance.CoordinatorCompletionReport{Synthesis: "All done."}, &api.CitationGrounding{HostAssembled: true}
			},
		},
		Projection: ProjectionDeps{
			UpdateMessage:      store.Update,
			AppendDraftVersion: func(context.Context, string, string, string, string) (int, error) { return 1, nil },
		},
	})
	st := &promptLoopTurnState{
		draftSlotID:       "draft",
		draftSlotAppended: true,
		turnStreamState: turnStreamState{
			offeredToolNames: []string{"update_progress", "promote_overlay"},
		},
	}
	if _, _, _, err := loop.Tools.tryRejectNoToolTurn(t.Context(), &api.Session{ID: "session"}, store.All(), "", draft.Content, guidance.CloseoutDraft{}, "implement_investigate", nil, "session", "draft", st, true, nil); err != nil {
		t.Fatalf("tryRejectNoToolTurn: %v", err)
	}
	if seenNoTool.Status("update_progress") != toolreach.Callable {
		t.Fatalf("BeforeFinishNoToolTurn reach = %+v", seenNoTool)
	}
	if _, err := loop.Closeout.emitAssembledCloseout(t.Context(), &api.Session{ID: "session"}, "session", "", "implement_investigate", markdownRead(draft.Content, false).Draft, nil, st, store.All()); err != nil {
		t.Fatalf("emitAssembledCloseout: %v", err)
	}
	if seenCompletion.Status("promote_overlay") != toolreach.Callable {
		t.Fatalf("AssertCompletion reach = %+v", seenCompletion)
	}
}

func TestCompletionBarrierRejectsOrdinaryAndAssembledProse(t *testing.T) {
	for _, assembled := range []bool{false, true} {
		name := "ordinary"
		if assembled {
			name = "assembled"
		}
		t.Run(name, func(t *testing.T) {
			refusal := guidance.NewRefusal("WORKFLOW_GATES_OPEN_BEFORE_CLOSEOUT", "Open workflow requirements").WithCause(guidance.ErrCloseoutStopped)
			store := &inPlaceStore{}
			draft := api.Message{ID: "draft", Role: api.MessageRoleAssistant, Content: "The host will advance.", DraftStatus: api.DraftStatusLive, Visibility: api.MessageVisibilityInternal}
			store.Append(draft)
			versions := 0
			loop := NewPromptLoopForTest(PromptLoopDeps{
				Closeout: CloseoutDeps{
					AssertCompletion: func(context.Context, *api.Session) error { return refusal },
					AssembleLedgerCloseout: func(context.Context, string, string, []string, guidance.CloseoutDraft, int) (guidance.CoordinatorCompletionReport, *api.CitationGrounding) {
						return guidance.CoordinatorCompletionReport{Synthesis: "The host will advance."}, &api.CitationGrounding{HostAssembled: true}
					},
				},
				Projection: ProjectionDeps{UpdateMessage: store.Update, AppendDraftVersion: func(context.Context, string, string, string, string) (int, error) { versions++; return versions, nil }},
			})
			var err error
			if assembled {
				st := &promptLoopTurnState{draftSlotID: "draft", draftSlotAppended: true}
				_, err = loop.Closeout.emitAssembledCloseout(t.Context(), &api.Session{ID: "session"}, "session", "", "implement_investigate", markdownRead(draft.Content, false).Draft, nil, st, store.All())
			} else {
				_, err = loop.Projection.commitGuardedAssistantTurn(t.Context(), &api.Session{ID: "session"}, "session", store.All(), "", "", draft)
			}
			if !errors.Is(err, guidance.ErrCloseoutStopped) {
				t.Fatalf("completion error=%v", err)
			}
			retained := store.Find("draft")
			if retained.Visibility != api.MessageVisibilityInternal || retained.DraftStatus != api.DraftStatusRejected || versions != 1 {
				t.Fatalf("rejected draft=%+v versions=%d", retained, versions)
			}
		})
	}
}
