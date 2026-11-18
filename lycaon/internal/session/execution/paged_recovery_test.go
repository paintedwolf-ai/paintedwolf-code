package execution

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/session/store/storetest"
	"github.com/lycaon/lycaon/internal/session/transcript"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestRecoverOrphanedTurnsDrainsMultiplePages(t *testing.T) {
	ctx := context.Background()
	sessionStore := storetest.New(t, testdbseed.DefaultProjectID)
	recovery := NewRecovery(sessionStore.Sessions, sessionStore.Turns, transcript.New(sessionStore.Sessions, sessionStore.Transcript, sessionStore.Turns, sessionStore.People, nil), NewStatus(sessionStore.Sessions, sessionStore.Transcript), nil)
	for i := 0; i < sessionRecoveryPageSize+7; i++ {
		sess, err := sessionStore.Sessions.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
		testutil.FailErr(t, "Create", err)
		testutil.FailErr(t, "set busy", sessionStore.Sessions.SetSessionStatus(ctx, sess.ID, api.SessionStatusBusy))
	}

	testutil.FailErr(t, "RecoverOrphanedTurns", recovery.RecoverOrphanedTurns(ctx))
	remaining, err := sessionStore.Sessions.ListBusySessionIDs(ctx, 1)
	testutil.FailErr(t, "ListBusySessionIDs", err)
	if len(remaining) != 0 {
		t.Fatalf("busy sessions remain after paged recovery: %v", remaining)
	}
}
