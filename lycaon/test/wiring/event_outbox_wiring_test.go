package wiring

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/worker"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// Mutations on outbox-bearing stores reach subscribers of the served hub, so
// each store enqueues into the outbox the event runtime built and started.
// Each mutation must first record an outbox row in its own transaction; a
// store without the outbox fails there instead of leaving the stream silent.
func TestOutboxBearingStoresDeliverThroughTheServedHubWiring(t *testing.T) {
	h := BuildForTest(t)
	ctx := context.Background()
	dir := t.TempDir()
	sess, err := h.CreateHarnessSession(t, wire.CreateSessionRequest{}, dir)
	testutil.FailErr(t, "create session", err)

	stream, unsubscribe, err := h.Events.Subscribe(ctx, events.Subscription{Project: sess.ProjectID, Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "subscribe", err)
	defer unsubscribe()

	before := outboxRowsEverWritten(t, h)
	testutil.FailErr(t, "append message", h.Store.AppendMessages(ctx, sess.ID, wire.Message{
		Role: wire.MessageRoleUser, Content: "outbox probe",
	}))
	before = requireOutboxRow(t, h, before, "session store")
	task := wire.WorkerTask{
		ParentSessionID: sess.ID, ProjectID: testdbseed.DefaultProjectID, WorkspacePath: dir,
		AgentType: "implementer", Prompt: "outbox probe", Brief: "outbox probe",
		Scope: &wire.TaskScope{Mode: wire.TaskScopeModeWrite, Paths: []string{"."}},
	}
	testutil.FailErr(t, "defaults", worker.ApplyEnqueueDefaults(&task, project.ProjectScope{ProjectID: testdbseed.DefaultProjectID, WorkspacePath: dir}, worker.DefaultWorkersConfig()))
	_, err = h.Workers.Queue.Enqueue(ctx, task)
	testutil.FailErr(t, "enqueue worker", err)
	requireOutboxRow(t, h, before, "worker queue")

	pending := map[wire.EventTopic]bool{wire.EventTopicMessage: true, wire.EventTopicWorker: true}
	for len(pending) > 0 {
		env, ok := <-stream
		if !ok {
			t.Fatalf("event stream closed with %v undelivered", pending)
		}
		delete(pending, env.Topic)
	}
}

// outboxRowsEverWritten reads the outbox's autoincrement high-water mark,
// which delivery does not lower when it deletes a row.
func outboxRowsEverWritten(t *testing.T, h *Harness) int64 {
	t.Helper()
	var seq int64
	err := h.DB.QueryRowContext(t.Context(), `SELECT COALESCE((SELECT seq FROM sqlite_sequence WHERE name = 'event_outbox'), 0)`).Scan(&seq)
	testutil.FailErr(t, "read outbox high-water mark", err)
	return seq
}

func requireOutboxRow(t *testing.T, h *Harness, before int64, store string) int64 {
	t.Helper()
	after := outboxRowsEverWritten(t, h)
	if after <= before {
		t.Fatalf("%s mutation recorded no outbox row", store)
	}
	return after
}
