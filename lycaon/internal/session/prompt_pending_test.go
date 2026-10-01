package session

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type escalatedGrounding struct{}

func (escalatedGrounding) IsEscalated(string) bool { return true }

func (escalatedGrounding) AfterPrompt(context.Context, string, []string) error { return nil }

func (escalatedGrounding) Reset(string) {}

// sessionEventsFor drains every delivered session event for one session.
func sessionEventsFor(t *testing.T, hub *events.MemoryHub, ch <-chan api.EventEnvelope, sessionID string) []api.SessionEvent {
	t.Helper()
	hub.FlushDebounced()
	var out []api.SessionEvent
	for {
		select {
		case envelope := <-ch:
			if envelope.Topic != api.EventTopicSession {
				continue
			}
			var event api.SessionEvent
			testutil.FailErr(t, "decode session event", json.Unmarshal(envelope.Data, &event))
			if event.ID == sessionID {
				out = append(out, event)
			}
		default:
			return out
		}
	}
}

func lastSessionEvent(t *testing.T, published []api.SessionEvent) api.SessionEvent {
	t.Helper()
	if len(published) == 0 {
		t.Fatal("no session event was published")
	}
	return published[len(published)-1]
}

// observePendingPrompts wires a publisher that reads session state from mgr.
func observePendingPrompts(t *testing.T, mgr *Manager, st *store.Memory) (*events.MemoryHub, *api.Session, <-chan api.EventEnvelope) {
	t.Helper()
	hub := events.NewMemoryHub()
	mgr.SetEventPublisher(&events.Publisher{Hub: hub, SessionState: mgr})
	sess, err := st.Create(t.Context(), api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	ch, unsubscribe, err := hub.Subscribe(t.Context(), events.Subscription{Project: sess.ProjectID, Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "subscribe to session events", err)
	t.Cleanup(unsubscribe)
	return hub, sess, ch
}

func TestIdleBoundaryBetweenPromptTurnsStatesTheWaitingPrompt(t *testing.T) {
	ctx := t.Context()
	mgr, st := newTestManager(t)
	hub, sess, ch := observePendingPrompts(t, mgr, st)

	// Attachments keep the first prompt out of the draft; the second waits behind it.
	firstInput := PromptInput{ContentParts: []api.MessageContentPart{{
		Content: "first", Origin: api.MessageOriginUser,
		Authority: api.ContentAuthorityUser, TrustTier: api.ContentTrustTierTrusted,
	}}}
	first, _, err := mgr.AdmitPrompt(ctx, sess.ID, uuid.NewString(), "first", firstInput)
	testutil.FailErr(t, "admit first", err)
	second, _, err := mgr.AdmitPrompt(ctx, sess.ID, uuid.NewString(), "second", PromptInput{Text: "second"})
	testutil.FailErr(t, "admit second", err)
	if !mgr.PromptPending(ctx, sess.ID) {
		t.Fatal("admitted prompts pend before any turn begins")
	}
	_, err = mgr.RunPromptSubmission(ctx, first.ID)
	testutil.FailErr(t, "run both prompts", err)

	var boundaries []api.SessionEvent
	for _, event := range sessionEventsFor(t, hub, ch, sess.ID) {
		if event.Status == api.SessionStatusIdle && event.IdleDisposition != "" {
			boundaries = append(boundaries, event)
		}
	}
	if len(boundaries) != 2 {
		t.Fatalf("idle boundaries = %+v, want one per turn", boundaries)
	}
	if !boundaries[0].PromptPending {
		t.Fatal("the first turn's idle boundary must state the prompt waiting to run next")
	}
	if boundaries[1].PromptPending {
		t.Fatal("the last turn's idle boundary leaves no prompt pending")
	}
	for _, id := range []string{first.ID, second.ID} {
		if mgr.begunSubmissions.has(id) {
			t.Fatalf("closed receipt %s is still tracked as begun", id)
		}
	}
}

func TestPromptRefusedBeforeItsTurnStopsPending(t *testing.T) {
	ctx := t.Context()
	mgr, st := newTestManager(t)
	hub, sess, ch := observePendingPrompts(t, mgr, st)
	mgr.SetGroundingHook(escalatedGrounding{})

	in := PromptInput{Text: "Continue."}
	row, _, err := mgr.AdmitPrompt(ctx, sess.ID, uuid.NewString(), in, in)
	testutil.FailErr(t, "admit prompt", err)
	if _, err := mgr.RunPromptSubmission(ctx, row.ID); !errors.Is(err, ErrGroundingEscalated) {
		t.Fatalf("run error = %v, want grounding escalation before the turn", err)
	}

	published := sessionEventsFor(t, hub, ch, sess.ID)
	if last := lastSessionEvent(t, published); last.PromptPending || last.Status != api.SessionStatusIdle || last.IdleDisposition != "" {
		t.Fatalf("session events = %+v, want an idle update with no prompt pending and no turn boundary", published)
	}
	if mgr.PromptPending(ctx, sess.ID) {
		t.Fatal("a refused prompt no longer pends")
	}
}

func TestPromptStopsPendingWhenItsTurnBegins(t *testing.T) {
	ctx := t.Context()
	mgr, st := newTestManager(t)
	hub, sess, ch := observePendingPrompts(t, mgr, st)
	mgr.llm = failingTurnClient{&failure.ProviderEmptyCompletionError{ProviderID: "fixture"}}

	in := PromptInput{Text: "Continue."}
	row, _, err := mgr.AdmitPrompt(ctx, sess.ID, uuid.NewString(), in, in)
	testutil.FailErr(t, "admit prompt", err)
	if _, err := mgr.RunPromptSubmission(ctx, row.ID); err == nil {
		t.Fatal("run succeeded, want the provider failure inside the turn")
	}

	published := sessionEventsFor(t, hub, ch, sess.ID)
	var sawBusy, sawBoundary bool
	for i, event := range published {
		if event.PromptPending {
			t.Fatalf("event %d = %+v: a prompt whose turn began no longer pends", i, event)
		}
		switch {
		case event.Status == api.SessionStatusBusy:
			if sawBoundary {
				t.Fatalf("turn became busy after its idle boundary: %+v", published)
			}
			sawBusy = true
		case event.Status == api.SessionStatusIdle && event.IdleDisposition != "":
			if sawBoundary {
				t.Fatalf("duplicate turn idle boundary: %+v", published)
			}
			sawBoundary = true
		}
	}
	if !sawBusy || !sawBoundary {
		t.Fatalf("session events = %+v, want busy and an idle boundary", published)
	}
	if mgr.begunSubmissions.has(row.ID) {
		t.Fatal("closed receipt is still tracked as begun")
	}
}

func TestAbandonedPromptFailsAndStopsPending(t *testing.T) {
	ctx := t.Context()
	mgr, st := newTestManager(t)
	hub, sess, ch := observePendingPrompts(t, mgr, st)

	in := PromptInput{Text: "Continue."}
	row, _, err := mgr.AdmitPrompt(ctx, sess.ID, uuid.NewString(), in, in)
	testutil.FailErr(t, "admit prompt", err)
	testutil.FailErr(t, "abandon submission", mgr.AbandonPromptSubmission(ctx, row.ID, errors.New("attachments unavailable")))

	stored, err := st.GetPromptSubmission(ctx, row.ID)
	testutil.FailErr(t, "read receipt", err)
	if stored.Status != store.PromptSubmissionFailed {
		t.Fatalf("receipt status = %s, want failed so recovery does not run it later", stored.Status)
	}
	published := sessionEventsFor(t, hub, ch, sess.ID)
	if len(published) != 1 || published[0].PromptPending {
		t.Fatalf("session events = %+v, want one update with no prompt pending", published)
	}

	// A receipt that is already terminal is left alone.
	testutil.FailErr(t, "abandon settled submission", mgr.AbandonPromptSubmission(ctx, row.ID, errors.New("again")))
	if again := sessionEventsFor(t, hub, ch, sess.ID); len(again) != 0 {
		t.Fatalf("session events = %+v, want none for a receipt that already closed", again)
	}
}

func TestDraftItemsPendUnlessHeldForEditing(t *testing.T) {
	ctx := t.Context()
	mgr, st := newTestManager(t)
	hub, sess, ch := observePendingPrompts(t, mgr, st)

	in := PromptInput{Text: "do this next"}
	row, _, err := mgr.AdmitPrompt(ctx, sess.ID, uuid.NewString(), in, in)
	testutil.FailErr(t, "admit prompt", err)
	dispatch := mgr.promptState.Submission.Acquire(sess.ID)
	dispatch.Lock()
	defer dispatch.Unlock()
	if _, err := mgr.RunPromptSubmission(ctx, row.ID); err != nil {
		t.Fatalf("route to the next-turn draft: %v", err)
	}
	if last := lastSessionEvent(t, sessionEventsFor(t, hub, ch, sess.ID)); !last.PromptPending {
		t.Fatalf("routed update = %+v, want an unheld draft item pending", last)
	}

	draft, err := mgr.QueueSetHold(ctx, sess.ID, mgr.QueueSnapshot(sess.ID).Revision, true)
	testutil.FailErr(t, "hold draft", err)
	if last := lastSessionEvent(t, sessionEventsFor(t, hub, ch, sess.ID)); last.PromptPending {
		t.Fatalf("held update = %+v, want a held draft item to wait on the person", last)
	}

	if _, err := mgr.QueueSend(ctx, sess.ID, draft.Revision); err != nil {
		t.Fatalf("reserve the held item: %v", err)
	}
	if last := lastSessionEvent(t, sessionEventsFor(t, hub, ch, sess.ID)); !last.PromptPending {
		t.Fatalf("send update = %+v, want the reserved item pending again", last)
	}
}
