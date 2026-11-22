package store

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestUserIntentBoundaryExcludesLaterTurnsAndFallsBackToCreation(t *testing.T) {
	database := testdbfixture.Open(t, "boundary.db")
	testdbseed.InsertProjectRoot(t, database, testdbseed.DefaultProjectID, t.TempDir())
	st := NewSQL(database)
	sess, err := st.Create(t.Context(), api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create root", err)
	spawned := time.Now().UTC().Truncate(time.Second)
	boundary, err := st.UserIntentBefore(t.Context(), sess.ID, spawned)
	testutil.FailErr(t, "empty boundary", err)
	if !boundary.Equal(spawned) {
		t.Fatal("missing opener widened history")
	}
	first := spawned.Add(-time.Minute)
	messages := []api.Message{
		{ID: uuid.NewString(), Role: api.MessageRoleUser, Origin: api.MessageOriginUser, Content: "first request", CreatedAt: first},
		{ID: uuid.NewString(), Role: api.MessageRoleUser, Origin: api.MessageOriginUser, Kind: api.MessageKindUserContinuation, Content: "steering", CreatedAt: first.Add(time.Second)},
		{ID: uuid.NewString(), Role: api.MessageRoleUser, Origin: api.MessageOriginUser, Content: "next request", CreatedAt: spawned.Add(time.Minute)},
	}
	testutil.FailErr(t, "append requests", st.AppendMessages(t.Context(), sess.ID, messages...))
	boundary, err = st.UserIntentBefore(t.Context(), sess.ID, spawned)
	testutil.FailErr(t, "historical boundary", err)
	if !boundary.Equal(first) {
		t.Fatalf("boundary = %v, want %v", boundary, first)
	}
}
