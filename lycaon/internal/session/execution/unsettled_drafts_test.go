package execution

import (
	"context"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/session/transcript"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

// recoveryStore is what both the boot and the per-session recovery read.
type recoveryStore interface {
	RecoveryStore
	StatusStore
	transcript.Store
	Create(context.Context, api.CreateSessionRequest, string) (*api.Session, error)
	BeginTurn(context.Context, store.TurnStart) (store.TurnExecution, error)
	SettleModelOutput(context.Context, store.ModelOutput) (store.ModelOutput, error)
}

// A live draft whose model output never settled holds no reply; recovery
// withdraws it from prompt history and keeps a draft whose output settled.
func TestRecoveryWithdrawsUnsettledLiveDrafts(t *testing.T) {
	for _, fixture := range []struct {
		name string
		open func(*testing.T) recoveryStore
	}{
		{name: "sql", open: func(t *testing.T) recoveryStore {
			database := testdbfixture.Open(t, "recover-drafts.db")
			testdbseed.InsertProjectRoot(t, database, testdbseed.DefaultProjectID, t.TempDir())
			return store.NewSQL(database)
		}},
	} {
		for _, scope := range []string{"boot", "session"} {
			t.Run(fixture.name+"/"+scope, func(t *testing.T) {
				ctx := t.Context()
				st := fixture.open(t)
				recovery := NewRecovery(st, transcript.New(st, nil), NewStatus(st), nil)
				sess, err := st.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
				testutil.FailErr(t, "create session", err)
				turn, err := st.BeginTurn(ctx, store.TurnStart{SessionID: sess.ID, ProjectID: sess.ProjectID, Origin: store.TurnOriginUser, InputJSON: "{}"})
				testutil.FailErr(t, "begin turn", err)
				live := func(id string) api.Message {
					return api.Message{ID: id, Role: api.MessageRoleAssistant, DraftStatus: api.DraftStatusLive, Visibility: api.MessageVisibilityInternal}
				}
				testutil.FailErr(t, "append drafts", st.AppendMessages(ctx, sess.ID, live("settled"), live("unsettled")))
				_, err = st.SettleModelOutput(ctx, store.ModelOutput{
					ID: "output-1", TurnAttemptID: turn.Attempt.ID, SessionID: sess.ID, Iteration: 1,
					MessageID: "settled", Content: "Reading.", ToolCallsJSON: "[]", ReasoningJSON: "null", CreatedAt: time.Now().UTC(),
				})
				testutil.FailErr(t, "settle output", err)

				for range 2 {
					if scope == "boot" {
						testutil.FailErr(t, "recover", recovery.RecoverOrphanedTurns(ctx))
					} else {
						testutil.FailErr(t, "recover session", recovery.RecoverOrphanedTurnsForSession(ctx, sess.ID))
					}
				}
				for id, wantPrompt := range map[string]bool{"settled": true, "unsettled": false} {
					msg, err := st.GetMessage(ctx, sess.ID, id)
					testutil.FailErr(t, "read "+id, err)
					if api.IsPromptHistoryMessage(msg) != wantPrompt {
						t.Errorf("draft %s in prompt history = %v, want %v (status %q)", id, !wantPrompt, wantPrompt, msg.DraftStatus)
					}
				}
			})
		}
	}
}
