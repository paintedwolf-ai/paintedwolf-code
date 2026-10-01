package store

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/messageview"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestCompactionAttemptSurvivesStoreReopen(t *testing.T) {
	database := testdbfixture.Open(t, "attempt.db")
	testdbseed.InsertProjectRoot(t, database, testdbseed.DefaultProjectID, t.TempDir())
	st := NewSQL(database)
	sess, err := st.Create(t.Context(), api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	want := messageview.CompactionAttempt{Revision: "exact-revision", Reason: "insufficient_savings"}
	testutil.FailErr(t, "save no-op", st.PutCompactionAttempt(t.Context(), sess.ID, want))
	got, found, err := NewSQL(database).GetCompactionAttempt(t.Context(), sess.ID)
	testutil.FailErr(t, "reload no-op", err)
	if !found || got != want {
		t.Fatalf("memo lost: %+v found=%v", got, found)
	}
	want.Revision = "new-revision"
	testutil.FailErr(t, "replace no-op", st.PutCompactionAttempt(t.Context(), sess.ID, want))
	got, _, err = st.GetCompactionAttempt(t.Context(), sess.ID)
	testutil.FailErr(t, "read replacement", err)
	if got != want {
		t.Fatal("memo did not advance")
	}
}

func TestCompactionPublicationRejectsStaleSourceAtomically(t *testing.T) {
	for _, backend := range []string{"memory", "sql"} {
		t.Run(backend, func(t *testing.T) {
			var st interface {
				compactionMutationStore
				Get(ctx context.Context, id string) (*api.Session, error)
			} = NewMemory()
			if backend == "sql" {
				database := testdbfixture.Open(t, "publication.db")
				testdbseed.InsertProjectRoot(t, database, testdbseed.DefaultProjectID, t.TempDir())
				st = NewSQL(database)
			}
			sess, err := st.Create(t.Context(), api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
			testutil.FailErr(t, "create session", err)
			testutil.FailErr(t, "append", st.AppendMessages(t.Context(), sess.ID, api.Message{ID: "source", Role: api.MessageRoleAssistant, Content: "earlier"}))
			rows, err := st.GetMessages(t.Context(), sess.ID)
			testutil.FailErr(t, "read source", err)
			row := rows[len(rows)-1]
			view := CompactionView{Generation: 1, CoveredThroughOrd: row.Ord, CoveredThroughID: row.ID, SourceSeq: row.Seq, Messages: []api.Message{{Content: "summary"}}}
			testutil.FailErr(t, "publish view", st.PutCompactionView(t.Context(), sess.ID, view))
			current, err := st.Get(t.Context(), sess.ID)
			testutil.FailErr(t, "read generation", err)
			if current.CompactionGeneration != 1 {
				t.Fatal("view published without generation")
			}
			_, err = st.UpdateMessage(t.Context(), sess.ID, row.ID, api.Message{Role: api.MessageRoleAssistant, Content: "corrected"})
			testutil.FailErr(t, "mutate source", err)
			view.Generation = 2
			if err := st.PutCompactionView(t.Context(), sess.ID, view); err == nil {
				t.Fatal("stale summary published")
			}
			current, err = st.Get(t.Context(), sess.ID)
			testutil.FailErr(t, "read generation after rejection", err)
			if current.CompactionGeneration == 2 {
				t.Fatal("failed publication advanced generation")
			}
		})
	}
}
