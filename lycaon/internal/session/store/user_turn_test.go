package store

import (
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestUserContinuationKeepsCurrentTurn(t *testing.T) {
	ctx := t.Context()
	database := testdbfixture.Open(t, "store.db")
	testdbseed.InsertProjectRoot(t, database, testdbseed.DefaultProjectID, t.TempDir())

	st := NewSQL(database)
	sess, err := st.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	testutil.FailErr(t, "append user messages", st.AppendMessages(ctx, sess.ID,
		api.Message{ID: uuid.NewString(), Role: api.MessageRoleUser, Origin: api.MessageOriginUser, Content: "start"},
		api.Message{ID: uuid.NewString(), Role: api.MessageRoleUser, Origin: api.MessageOriginUser, Kind: api.MessageKindUserContinuation, Content: "adjust"},
	))
	turnSource, err := st.ReadSourceTurn(ctx, sess.ID, 0)
	turn := turnSource.Number
	testutil.FailErr(t, "read user turn ordinal", err)
	if turn != 1 {
		t.Fatalf("user turn ordinal = %d want 1", turn)
	}
	testutil.FailErr(t, "append assistant observation", st.AppendMessages(ctx, sess.ID,
		api.Message{ID: uuid.NewString(), Role: api.MessageRoleAssistant, Content: "observed"},
		api.Message{ID: uuid.NewString(), Role: api.MessageRoleUser, Content: "second instruction"}))
	second, err := st.ReadSourceTurn(ctx, sess.ID, 2)
	testutil.FailErr(t, "resolve source turn identity", err)
	opener, err := st.GetMessage(ctx, sess.ID, second.OpeningMessageID)
	testutil.FailErr(t, "resolve source turn opener", err)
	if opener.Content != "second instruction" || opener.Ord != 4 {
		t.Fatal("source turn identity was treated as a transcript ordinal")
	}
	if _, err := st.ReadSourceTurn(ctx, sess.ID, -1); err == nil {
		t.Fatal("negative source turn accepted")
	}
	missing, err := st.ReadSourceTurn(ctx, sess.ID, 999)
	testutil.FailErr(t, "read absent source turn", err)
	if missing.OpeningMessageID != "" {
		t.Fatal("absent source turn returned an opener")
	}
}

func TestTurnNumbersAreNotReusedAfterTranscriptTruncation(t *testing.T) {
	database := testdbfixture.Open(t, "turns.db")
	testdbseed.InsertProjectRoot(t, database, testdbseed.DefaultProjectID, t.TempDir())
	st := NewSQL(database)
	ctx := t.Context()
	sess, err := st.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	first := api.Message{ID: uuid.NewString(), Role: api.MessageRoleUser, Origin: api.MessageOriginUser, Content: "first"}
	second := first
	second.ID = uuid.NewString()
	testutil.FailErr(t, "append turns", st.AppendMessages(ctx, sess.ID, first, second))
	rewindFrom(t, st, sess.ID, first.ID)
	empty, err := st.Get(ctx, sess.ID)
	testutil.FailErr(t, "read rewound session", err)
	if empty.CurrentTurn != 0 {
		t.Fatalf("empty current turn=%d", empty.CurrentTurn)
	}
	next := first
	next.ID = uuid.NewString()
	testutil.FailErr(t, "append next turn", st.AppendMessages(ctx, sess.ID, next))
	currentSource, err := st.ReadSourceTurn(ctx, sess.ID, 0)
	current := currentSource.Number
	testutil.FailErr(t, "read permanent turn", err)
	if current != 3 {
		t.Fatalf("next turn=%d, want 3", current)
	}
}
