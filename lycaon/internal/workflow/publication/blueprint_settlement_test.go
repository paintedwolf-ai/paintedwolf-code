package publication_test

import (
	"encoding/json"
	"testing"

	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workflow"
	"github.com/lycaon/lycaon/internal/workflow/persistence"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestBlueprintRebindPublishesOnePatchForCommittedRow(t *testing.T) {
	ctx := t.Context()
	database := testdbfixture.Open(t, "blueprint.db")
	const sessionID, projectID = "chat", testdbseed.DefaultProjectID
	const from, to = ".paintedwolf/blueprints/draft.md", ".paintedwolf/blueprints/design.md"
	testdbseed.InsertWorkflowRun(t, database, "run", sessionID, projectID)
	_, err := database.ExecContext(ctx, "UPDATE workflow_runs SET status = 'complete', completed_at = updated_at, blueprint_path = ? WHERE id = 'run'", from)
	testutil.FailErr(t, "bind source blueprint", err)
	hub := events.NewMemoryHub()
	pub := &events.Publisher{Hub: hub}
	mgr := workflow.NewManager(persistence.New(database), store.NewSQL(database), nil, pub)
	msgs, stop, err := hub.Subscribe(ctx, events.Subscription{Project: projectID, Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "subscribe to project", err)
	t.Cleanup(stop)
	row := api.Message{ID: "card", Role: api.MessageRoleSystem, Kind: api.MessageKindBlueprint, Content: "Design", Blueprint: &api.BlueprintMeta{BlueprintPath: from}}
	testutil.FailErr(t, "append card", mgr.Transcript.Append(ctx, sessionID, row))
	if err := mgr.Transcript.Append(ctx, sessionID, row); err == nil {
		t.Fatal("duplicate card committed")
	}
	for range 2 {
		testutil.FailErr(t, "rebind blueprint", mgr.Blueprints.RebindBlueprintPath(ctx, projectID, from, to))
	}
	pub.FlushMessagePatches(ctx, sessionID)
	hub.FlushDebounced()
	var changes []api.MessageEvent
	for {
		select {
		case envelope := <-msgs:
			if envelope.Topic != api.EventTopicMessage {
				continue
			}
			var event api.MessageEvent
			testutil.FailErr(t, "decode message event", json.Unmarshal(envelope.Data, &event))
			if envelope.Scope.ProjectID != projectID || event.SessionID != sessionID || event.Message.ID != row.ID || event.Message.Blueprint == nil {
				t.Fatalf("misdirected blueprint event: %+v", envelope)
			}
			changes = append(changes, event)
		default:
			if len(changes) != 2 || changes[0].Op != api.MessageChangeAppend || changes[0].Message.Blueprint.BlueprintPath != from || changes[1].Op != api.MessageChangePatch || changes[1].Message.Blueprint.BlueprintPath != to {
				t.Fatalf("blueprint changes: %+v", changes)
			}
			run, err := mgr.Store.Runs.Get(ctx, "run")
			testutil.FailErr(t, "read rebound run", err)
			if run.BlueprintPath != to {
				t.Fatalf("persisted blueprint = %q", run.BlueprintPath)
			}
			return
		}
	}
}
