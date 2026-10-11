package promptloop

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/pkg/api"
)

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
					AssembleLedgerCloseout: func(context.Context, string, string, []string, string, int) (guidance.CoordinatorCompletionReport, *api.CitationGrounding) {
						return guidance.CoordinatorCompletionReport{Synthesis: "The host will advance."}, &api.CitationGrounding{HostAssembled: true}
					},
				},
				Projection: ProjectionDeps{UpdateMessage: store.Update, AppendDraftVersion: func(context.Context, string, string, string, string) (int, error) { versions++; return versions, nil }},
			})
			var err error
			if assembled {
				st := &promptLoopTurnState{draftSlotID: "draft", draftSlotAppended: true}
				_, err = loop.Closeout.emitAssembledCloseout(t.Context(), &api.Session{ID: "session"}, "session", "", "implement_investigate", draft.Content, nil, st, store.All())
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
