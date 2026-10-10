package session_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestStampMessageLiveStatusStreamingOnlyForActiveRow(t *testing.T) {
	ctx := t.Context()
	store := store.NewMemory()
	mgr := session.NewHost(store, session.Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, nil)
	sess, err := store.Create(ctx, api.CreateSessionRequest{
		ProjectID: "p1",
		Posture:   api.SessionPostureBuild,
	}, "p1")
	testutil.FailErr(t, "Create session", err)

	mgr.Runner.Transcript.Streams.SetActive(sess.ID, "live-row", 512)

	msgs := []api.Message{
		{ID: "u1", Role: api.MessageRoleUser, Content: "hi", CreatedAt: sess.CreatedAt},
		{ID: "live-row", Role: api.MessageRoleAssistant, Content: "partial", CreatedAt: sess.CreatedAt},
		{ID: "done-row", Role: api.MessageRoleAssistant, Content: "done", CreatedAt: sess.CreatedAt},
	}
	testutil.FailErr(t, "AppendMessages", store.AppendMessages(ctx, sess.ID, msgs...))

	page, err := mgr.Runner.Transcript.GetTranscriptPage(ctx, sess.ID, api.TranscriptPageQuery{})
	testutil.FailErr(t, "GetTranscriptPage", err)
	if len(page.Messages) != 3 {
		t.Fatalf("messages = %d want 3", len(page.Messages))
	}
	for _, msg := range page.Messages {
		switch msg.ID {
		case "live-row":
			if msg.Status != api.MessageLiveStatusStreaming {
				t.Fatalf("live-row status = %q want streaming", msg.Status)
			}
			if msg.GeneratingTokens != 512 {
				t.Fatalf("live-row generating_tokens = %d want 512", msg.GeneratingTokens)
			}
		case "u1", "done-row":
			if msg.Status != api.MessageLiveStatusComplete {
				t.Fatalf("%s status = %q want complete", msg.ID, msg.Status)
			}
			if msg.GeneratingTokens != 0 {
				t.Fatalf("%s generating_tokens = %d want 0 (only live rows carry it)", msg.ID, msg.GeneratingTokens)
			}
		default:
			t.Fatalf("unexpected message id %q", msg.ID)
		}
	}
}

func TestHydrationYieldsCompleteWhenNoActiveTurn(t *testing.T) {
	ctx := t.Context()
	store := store.NewMemory()
	mgr := session.NewHost(store, session.Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, nil)
	sess, err := store.Create(ctx, api.CreateSessionRequest{
		ProjectID: "p1",
		Posture:   api.SessionPostureBuild,
	}, "p1")
	testutil.FailErr(t, "Create session", err)

	testutil.FailErr(t, "AppendMessages", store.AppendMessages(ctx, sess.ID, api.Message{
		ID:          "a1",
		Role:        api.MessageRoleAssistant,
		Content:     "stuck live body",
		DraftStatus: api.DraftStatusLive,
		CreatedAt:   sess.CreatedAt,
	}))

	page, err := mgr.Runner.Transcript.GetTranscriptPage(ctx, sess.ID, api.TranscriptPageQuery{})
	testutil.FailErr(t, "GetTranscriptPage", err)
	if len(page.Messages) != 1 {
		t.Fatalf("messages = %d want 1", len(page.Messages))
	}
	if page.Messages[0].Status != api.MessageLiveStatusComplete {
		t.Fatalf("status = %q want complete on idle hydration", page.Messages[0].Status)
	}
}
