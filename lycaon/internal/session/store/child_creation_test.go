package store

import (
	"testing"

	"github.com/lycaon/lycaon/internal/eventoutbox"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestChildCreationRollsBackWithInitialTranscript(t *testing.T) {
	repository := newSQLTestStore(t)
	ctx := t.Context()
	repository.SetEventOutbox(eventoutbox.New(repository.core.db, events.NewMemoryHub()))
	parent, err := repository.Sessions.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create parent", err)
	_, err = repository.core.db.ExecContext(ctx, `CREATE TRIGGER reject_initial_prompt BEFORE INSERT ON messages BEGIN SELECT RAISE(ABORT, 'initial prompt rejected'); END`)
	testutil.FailErr(t, "reject initial prompt", err)
	request := api.SpawnChildRequest{AgentType: "implementer", Prompt: "start work"}
	if child, err := repository.Sessions.CreateChild(ctx, parent, request); err == nil || child != nil {
		t.Fatalf("failed child creation = %v, %v", child, err)
	}
	var children, pendingEvents int
	err = repository.core.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions WHERE parent_session_id = ?`, parent.ID).Scan(&children)
	testutil.FailErr(t, "count children after rejection", err)
	err = repository.core.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM event_outbox WHERE session_id != ?`, parent.ID).Scan(&pendingEvents)
	testutil.FailErr(t, "count child events after rejection", err)
	if children != 0 || pendingEvents != 0 {
		t.Fatalf("rejected creation retained children=%d events=%d", children, pendingEvents)
	}
	_, err = repository.core.db.ExecContext(ctx, `DROP TRIGGER reject_initial_prompt`)
	testutil.FailErr(t, "allow initial prompt", err)
	child, err := repository.Sessions.CreateChild(ctx, parent, request)
	testutil.FailErr(t, "retry child creation", err)
	messages, err := repository.Transcript.GetMessages(ctx, child.ID)
	testutil.FailErr(t, "read initial transcript", err)
	if len(messages) != 1 || messages[0].Content != request.Prompt || messages[0].Ord != 1 {
		t.Fatalf("initial transcript = %+v", messages)
	}
	err = repository.core.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM event_outbox WHERE session_id = ?`, child.ID).Scan(&pendingEvents)
	testutil.FailErr(t, "count committed child events", err)
	if pendingEvents != 2 {
		t.Fatalf("committed child events = %d, want creation and transcript", pendingEvents)
	}
}
