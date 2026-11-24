package store_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/session/store/storetest"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestCompletionCueStatePersistsAcrossReadsAndResets(t *testing.T) {
	ctx := t.Context()
	repos := storetest.New(t, testdbseed.DefaultProjectID)
	sess, err := repos.Sessions.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)

	initial, err := repos.CompletionCues.GetCompletionCueState(ctx, sess.ID)
	testutil.FailErr(t, "read missing completion cue state", err)
	if initial != (store.CompletionCueState{}) {
		t.Fatalf("expected zero state, got %+v", initial)
	}

	want := store.CompletionCueState{
		IntentID:           "intent-1",
		OutcomeDelivered:   true,
		FinalItemDelivered: false,
	}
	testutil.FailErr(t, "save completion cue state", repos.CompletionCues.SaveCompletionCueState(ctx, sess.ID, want))

	got, err := repos.CompletionCues.GetCompletionCueState(ctx, sess.ID)
	testutil.FailErr(t, "read saved completion cue state", err)
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}

	reset := store.CompletionCueState{
		IntentID:           "intent-2",
		OutcomeDelivered:   false,
		FinalItemDelivered: false,
	}
	testutil.FailErr(t, "reset completion cue state", repos.CompletionCues.SaveCompletionCueState(ctx, sess.ID, reset))

	got, err = repos.CompletionCues.GetCompletionCueState(ctx, sess.ID)
	testutil.FailErr(t, "read reset completion cue state", err)
	if got != reset {
		t.Fatalf("got %+v, want %+v", got, reset)
	}
}
