package store

import (
	"context"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
	"testing"
)

func TestHostSignalIDRoundTrips(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "host-signal.db")
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, t.TempDir())
	store := NewSQL(sqlDB)
	sess, err := store.Create(context.Background(), api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)

	msg := api.Message{
		Role:         api.MessageRoleUser,
		Origin:       api.MessageOriginHost,
		Kind:         api.MessageKindHostKick,
		HostSignalID: "worker.task.finished",
	}
	testutil.FailErr(t, "append host signal", store.AppendMessages(context.Background(), sess.ID, msg))
	got, err := store.GetMessages(context.Background(), sess.ID)
	testutil.FailErr(t, "get messages", err)
	if len(got) != 1 || got[0].HostSignalID != msg.HostSignalID {
		t.Fatalf("round trip = %+v", got)
	}

	patched := got[0]
	patched.Kind = api.MessageKindCoordinatorGuidance
	patched.HostSignalID = "PROGRESS_MISSING"
	_, err = store.UpdateMessage(context.Background(), sess.ID, patched.ID, patched)
	testutil.FailErr(t, "update host signal", err)
	got, err = store.GetMessages(context.Background(), sess.ID)
	testutil.FailErr(t, "get updated messages", err)
	if got[0].Kind != patched.Kind || got[0].HostSignalID != patched.HostSignalID {
		t.Fatalf("updated round trip = %+v", got[0])
	}
}
