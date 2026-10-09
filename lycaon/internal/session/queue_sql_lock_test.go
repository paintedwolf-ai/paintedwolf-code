package session

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/eventoutbox"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/session/promptinput"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

type gatedPromptPending struct {
	manager    *Host
	writerHeld chan struct{}
	queueHeld  chan struct{}
}

func (p *gatedPromptPending) PromptPendingAmong(sessionID string, ids []string) bool {
	close(p.writerHeld)
	<-p.queueHeld
	return p.manager.Runner.SubmissionState.PromptPendingAmong(sessionID, ids)
}

func TestQueuedReceiptClaimDoesNotDeadlockSessionSeenPublication(t *testing.T) {
	database := testdbfixture.Open(t, "queue-seen.db")
	testdbseed.InsertProjectRoot(t, database, testdbseed.DefaultProjectID, t.TempDir())
	st := store.NewSQL(database)
	st.SetEventOutbox(eventoutbox.New(database, events.NewMemoryHub()))
	manager := NewHost(st, Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	sess, err := st.Create(t.Context(), api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	row, _, err := manager.Submissions.AdmitPrompt(t.Context(), sess.ID, uuid.NewString(), "queued", promptinput.Input{Text: "queued"})
	testutil.FailErr(t, "admit queued prompt", err)
	manager.queue.AppendOrdered(sess.ID, row.ID, row.SubmittedBy, "queued", row.AdmissionSeq, row.CreatedAt)
	pending := &gatedPromptPending{manager: manager, writerHeld: make(chan struct{}), queueHeld: make(chan struct{})}
	st.SetPromptPending(pending)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	seen := make(chan error, 1)
	go func() {
		seen <- st.UpdateSession(ctx, sess.ID, func(s *api.Session) { now := time.Now(); s.SeenAt = &now })
	}()
	select {
	case <-pending.writerHeld:
	case <-ctx.Done():
		t.Fatal("seen update never acquired writer")
	}
	_, taken, claimErr := manager.queue.TakeNextTurn(sess.ID, func(items []api.QueueItem) error {
		close(pending.queueHeld)
		_, claimed, err := st.ClaimPromptSubmission(ctx, items[0].ID)
		if err == nil && !claimed {
			t.Error("queued receipt was not claimed")
		}
		return err
	})
	testutil.FailErr(t, "claim queued receipt with seen writer active", claimErr)
	testutil.FailErr(t, "publish seen state during queue claim", <-seen)
	if !taken {
		t.Fatal("queue did not drain")
	}
}
