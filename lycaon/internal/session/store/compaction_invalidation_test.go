package store

import (
	"fmt"
	"testing"

	"github.com/lycaon/lycaon/internal/messageview"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestCompactionRevisionLagRebuildsWithoutChangingHistory(t *testing.T) {

	database := testdbfixture.Open(t, "revision-gap.db")
	testdbseed.InsertProjectRoot(t, database, testdbseed.DefaultProjectID, t.TempDir())
	st := NewSQL(database)

	sess, err := st.Create(t.Context(), api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	if err := st.PutCompactionView(t.Context(), sess.ID, CompactionView{Generation: 1, Messages: make([]api.Message, messageview.MaxCompactionRows+1)}); err == nil {
		t.Fatal("published an unbounded projection")
	}
	testutil.FailErr(t, "append boundary", st.AppendMessages(t.Context(), sess.ID, api.Message{ID: "boundary", Role: api.MessageRoleAssistant, Content: "recorded"}))
	rows, err := st.GetMessages(t.Context(), sess.ID)
	testutil.FailErr(t, "read boundary", err)
	view := CompactionView{Generation: 1, CoveredThroughOrd: rows[0].Ord, CoveredThroughID: rows[0].ID, SourceSeq: rows[0].Seq}
	testutil.FailErr(t, "publish view", st.PutCompactionView(t.Context(), sess.ID, view))
	suffix := make([]api.Message, messageview.MaxCompactionRevisionLag+1)
	for i := range suffix {
		suffix[i] = api.Message{ID: fmt.Sprintf("suffix-%d", i), Role: api.MessageRoleAssistant, Content: "later"}
	}
	testutil.FailErr(t, "append suffix", st.AppendMessages(t.Context(), sess.ID, suffix...))
	current, err := st.CompactionViewCurrent(t.Context(), sess.ID, view.CoveredThroughOrd, view.CoveredThroughID, view.SourceSeq)
	testutil.FailErr(t, "validate lagged view", err)
	if current {
		t.Fatal("lagged projection remained eligible for source validation")
	}
	view.Generation++
	if err := st.PutCompactionView(t.Context(), sess.ID, view); err == nil {
		t.Fatal("published projection beyond the revision budget")
	}
	rows, err = st.GetMessages(t.Context(), sess.ID)
	testutil.FailErr(t, "read canonical history", err)
	if len(rows) != len(suffix)+1 || rows[0].Content != "recorded" {
		t.Fatal("projection rebuild changed canonical history")
	}

}

func TestContinuationCopiesSlices(t *testing.T) {
	sessionStore := newSQLTestStore(t)
	sess, err := sessionStore.Create(t.Context(), api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	record := &messageview.Continuation{Summary: messageview.ContinuationSummary{Facts: []string{"accepted"}}, Sources: []messageview.ContinuationSource{{Handles: []string{"read#1"}}}}
	testutil.FailErr(t, "publish continuation", sessionStore.PutCompactionView(t.Context(), sess.ID, CompactionView{Generation: 1, Continuation: record}))
	record.Summary.Facts[0] = "changed"
	view, _, err := sessionStore.GetCompactionView(t.Context(), sess.ID)
	testutil.FailErr(t, "read continuation", err)
	view.Continuation.Sources[0].Handles[0] = "changed"
	view, _, err = sessionStore.GetCompactionView(t.Context(), sess.ID)
	testutil.FailErr(t, "reread continuation", err)
	if view.Continuation.Summary.Facts[0] != "accepted" || view.Continuation.Sources[0].Handles[0] != "read#1" {
		t.Fatal("consumer mutated the stored continuation")
	}
}

func TestCompactionViewInvalidatesOnlyCoveredMessageMutations(t *testing.T) {

	for _, streaming := range []bool{false, true} {
		name := "committed"
		if streaming {
			name = "streaming"
		}
		t.Run(name, func(t *testing.T) {
			database := testdbfixture.Open(t, "compaction.db")
			testdbseed.InsertProjectRoot(t, database, testdbseed.DefaultProjectID, t.TempDir())
			st := NewSQL(database)

			checkCompactionMutationBoundary(t, st, streaming)
		})
	}

}

func checkCompactionMutationBoundary(t *testing.T, st *SQL, streaming bool) {
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
