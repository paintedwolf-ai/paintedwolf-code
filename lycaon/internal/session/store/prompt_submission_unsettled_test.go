package store

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/eventoutbox"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type unsettledPromptStore interface {
	Create(context.Context, api.CreateSessionRequest, string) (*api.Session, error)
	PutPromptSubmission(context.Context, PromptSubmission) (*PromptSubmission, bool, error)
	ClaimPromptSubmission(context.Context, string) (*PromptSubmission, bool, error)
	FinishPromptSubmission(context.Context, string, string, PromptSubmissionStatus, string, PromptSubmissionFailure) error
	CancelQueuedPromptSubmissions(context.Context, []string) error
	ListUnsettledUserPromptSubmissionIDs(context.Context, string) ([]string, error)
}

func admitTestPrompt(t *testing.T, st unsettledPromptStore, sess *api.Session, origin PromptSubmissionOrigin) *PromptSubmission {
	t.Helper()
	sender := ""
	if origin == PromptSubmissionOriginUser {
		sender = sess.OwnerPersonID
	}
	row, _, err := st.PutPromptSubmission(t.Context(), PromptSubmission{
		ID: uuid.NewString(), SessionID: sess.ID, ProjectID: sess.ProjectID,
		InputDigest: "digest", InputJSON: `{}`, Origin: origin, SubmittedBy: sender,
	})
	testutil.FailErr(t, "admit prompt", err)
	return row
}

func TestUnsettledUserPromptsAreQueuedOrRunningInAdmissionOrder(t *testing.T) {
	for _, fixture := range []struct {
		name string
		open func(*testing.T) unsettledPromptStore
	}{
		{name: "memory", open: func(*testing.T) unsettledPromptStore { return NewMemory() }},
		{name: "sql", open: func(t *testing.T) unsettledPromptStore {
			handle := testdbfixture.Open(t, "unsettled.db")
			testdbseed.InsertProjectRoot(t, handle, testdbseed.DefaultProjectID, t.TempDir())
			return NewSQL(handle)
		}},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			st := fixture.open(t)
			sess, err := st.Create(t.Context(), api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
			testutil.FailErr(t, "create session", err)

			running := admitTestPrompt(t, st, sess, PromptSubmissionOriginUser)
			completed := admitTestPrompt(t, st, sess, PromptSubmissionOriginUser)
			canceled := admitTestPrompt(t, st, sess, PromptSubmissionOriginUser)
			admitTestPrompt(t, st, sess, PromptSubmissionOriginLoopWake)
			queued := admitTestPrompt(t, st, sess, PromptSubmissionOriginUser)

			_, claimed, err := st.ClaimPromptSubmission(t.Context(), running.ID)
			testutil.FailErr(t, "claim running prompt", err)
			if !claimed {
				t.Fatal("running prompt was not claimable")
			}
			done, _, err := st.ClaimPromptSubmission(t.Context(), completed.ID)
			testutil.FailErr(t, "claim completed prompt", err)
			testutil.FailErr(t, "complete prompt", st.FinishPromptSubmission(t.Context(), done.ID, done.ClaimToken,
				PromptSubmissionComplete, "", PromptSubmissionFailure{}))
			testutil.FailErr(t, "cancel prompt", st.CancelQueuedPromptSubmissions(t.Context(), []string{canceled.ID}))

			got, err := st.ListUnsettledUserPromptSubmissionIDs(t.Context(), sess.ID)
			testutil.FailErr(t, "list unsettled prompts", err)
			if want := []string{running.ID, queued.ID}; !slices.Equal(got, want) {
				t.Fatalf("unsettled = %v, want the running and queued human prompts %v", got, want)
			}
		})
	}
}

type recordingPromptPending struct {
	sessionID string
	unsettled []string
}

func (r *recordingPromptPending) PromptPendingAmong(sessionID string, unsettled []string) bool {
	r.sessionID, r.unsettled = sessionID, unsettled
	return len(unsettled) > 0
}

// A status commit's outbox event states prompt_pending from the same
// transaction, so the relay can never deliver a status without it.
func TestSQLSessionEventStatesPromptPendingFromItsTransaction(t *testing.T) {
	database := testdbfixture.Open(t, "store.db")
	testdbseed.InsertProjectRoot(t, database, testdbseed.DefaultProjectID, t.TempDir())
	st := NewSQL(database)
	st.SetEventOutbox(eventoutbox.New(database, events.NewMemoryHub()))
	pending := &recordingPromptPending{}
	st.SetPromptPending(pending)

	sess, err := st.Create(t.Context(), api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	prompt := admitTestPrompt(t, st, sess, PromptSubmissionOriginUser)

	latestEvent := func() api.SessionEvent {
		t.Helper()
		var raw string
		testutil.FailErr(t, "read outbox row", database.QueryRowContext(t.Context(),
			`SELECT data_json FROM event_outbox WHERE topic = ? AND session_id = ? ORDER BY id DESC LIMIT 1`,
			string(api.EventTopicSession), sess.ID,
		).Scan(&raw))
		var event api.SessionEvent
		testutil.FailErr(t, "decode outbox session event", json.Unmarshal([]byte(raw), &event))
		return event
	}

	testutil.FailErr(t, "mark busy", st.SetSessionStatus(t.Context(), sess.ID, api.SessionStatusBusy))
	if event := latestEvent(); !event.PromptPending || event.Status != api.SessionStatusBusy {
		t.Fatalf("busy event = %+v, want prompt_pending while the prompt is unsettled", event)
	}
	if pending.sessionID != sess.ID || !slices.Equal(pending.unsettled, []string{prompt.ID}) {
		t.Fatalf("source saw %s %v, want %s [%s]", pending.sessionID, pending.unsettled, sess.ID, prompt.ID)
	}

	claimed, _, err := st.ClaimPromptSubmission(t.Context(), prompt.ID)
	testutil.FailErr(t, "claim prompt", err)
	testutil.FailErr(t, "complete prompt", st.FinishPromptSubmission(t.Context(), claimed.ID, claimed.ClaimToken,
		PromptSubmissionComplete, "", PromptSubmissionFailure{}))
	testutil.FailErr(t, "mark idle", st.SetSessionStatus(t.Context(), sess.ID, api.SessionStatusIdle))
	if event := latestEvent(); event.PromptPending {
		t.Fatalf("idle event = %+v, want no prompt pending once the receipt closed", event)
	}
}
