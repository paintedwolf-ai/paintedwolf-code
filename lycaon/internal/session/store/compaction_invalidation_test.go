package store

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type compactionMutationStore interface {
	Create(context.Context, api.CreateSessionRequest, string) (*api.Session, error)
	AppendMessages(context.Context, string, ...api.Message) error
	GetMessages(context.Context, string) ([]api.Message, error)
	PutCompactionView(context.Context, string, CompactionView) error
	GetCompactionView(context.Context, string) (*CompactionView, bool, error)
	UpdateMessage(context.Context, string, string, api.Message) (api.Message, error)
	PatchLiveProjection(context.Context, string, string, string, []api.ToolCall) error
}

func TestCompactionViewInvalidatesOnlyCoveredMessageMutations(t *testing.T) {
	for _, backend := range []string{"memory", "sql"} {
		for _, streaming := range []bool{false, true} {
			name := backend + "/committed"
			if streaming {
				name = backend + "/streaming"
			}
			t.Run(name, func(t *testing.T) {
				var st compactionMutationStore = NewMemory()
				if backend == "sql" {
					database := testdbfixture.Open(t, "compaction.db")
					testdbseed.InsertProjectRoot(t, database, testdbseed.DefaultProjectID, t.TempDir())
					st = NewSQL(database)
				}
				checkCompactionMutationBoundary(t, st, streaming)
			})
		}
	}
}

func checkCompactionMutationBoundary(t *testing.T, st compactionMutationStore, streaming bool) {
	t.Helper()
	ctx := t.Context()
	sess, err := st.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	testutil.FailErr(t, "append history", st.AppendMessages(ctx, sess.ID,
		api.Message{ID: "covered", Role: api.MessageRoleUser, Content: "original"},
		api.Message{ID: "suffix", Role: api.MessageRoleAssistant, Content: "new reply"}))
	msgs, err := st.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "get history", err)
	testutil.FailErr(t, "compact prefix", st.PutCompactionView(ctx, sess.ID, CompactionView{
		Generation: 1, CoveredThroughOrd: msgs[0].Ord, CoveredThroughID: msgs[0].ID,
		SourceSeq: msgs[1].Seq, Messages: []api.Message{{ID: "summary", Role: api.MessageRoleSystem, Content: "summary"}},
	}))
	for _, mutation := range []struct {
		id       string
		wantView bool
	}{{"suffix", true}, {"covered", false}} {
		if streaming {
			err = st.PatchLiveProjection(ctx, sess.ID, mutation.id, "updated", nil)
		} else {
			_, err = st.UpdateMessage(ctx, sess.ID, mutation.id, api.Message{ID: mutation.id, Role: api.MessageRoleAssistant, Content: "updated"})
		}
		testutil.FailErr(t, "mutate message", err)
		view, exists, err := st.GetCompactionView(ctx, sess.ID)
		testutil.FailErr(t, "get compaction view", err)
		if exists != mutation.wantView {
			t.Fatalf("mutation %s: view exists=%v, want %v", mutation.id, exists, mutation.wantView)
		}
		if exists && (view.Generation != 1 || view.Messages[0].Content != "summary") {
			t.Fatalf("suffix mutation changed compaction view: %+v", view)
		}
	}
}
