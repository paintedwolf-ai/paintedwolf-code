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
func TestOutboxBearingStoresDeliverThroughTheServedHubWiring(t *testing.T) {
	h := BuildForTest(t)
	ctx := context.Background()
	dir := t.TempDir()
	sess, err := h.CreateHarnessSession(t, wire.CreateSessionRequest{}, dir)
	testutil.FailErr(t, "create session", err)

	stream, unsubscribe, err := h.Events.Subscribe(ctx, events.Subscription{Project: sess.ProjectID, Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "subscribe", err)
	defer unsubscribe()

	testutil.FailErr(t, "append message", h.Store.AppendMessages(ctx, sess.ID, wire.Message{
		Role: wire.MessageRoleUser, Content: "outbox probe",
	}))
	task := wire.WorkerTask{
		ParentSessionID: sess.ID, ProjectID: testdbseed.DefaultProjectID, WorkspacePath: dir,
		AgentType: "implementer", Prompt: "outbox probe", Brief: "outbox probe",
		Scope: &wire.TaskScope{Mode: wire.TaskScopeModeWrite, Paths: []string{"."}},
	}
	testutil.FailErr(t, "defaults", worker.ApplyEnqueueDefaults(&task, project.ProjectScope{ProjectID: testdbseed.DefaultProjectID, WorkspacePath: dir}, worker.DefaultWorkersConfig()))
	_, err = h.Workers.Queue.Enqueue(ctx, task)
	testutil.FailErr(t, "enqueue worker", err)

	pending := map[wire.EventTopic]bool{wire.EventTopicMessage: true, wire.EventTopicWorker: true}
	for len(pending) > 0 {
		env, ok := <-stream
		if !ok {
			t.Fatalf("event stream closed with %v undelivered", pending)
		}
		delete(pending, env.Topic)
	}
}
