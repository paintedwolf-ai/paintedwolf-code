package workflow

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	workflowpersistence "github.com/lycaon/lycaon/internal/workflow/persistence"
	"github.com/lycaon/lycaon/pkg/api"
)

func newRunManagerForEvents(t *testing.T, name string) (*RunManager, *events.MemoryHub, *store.SQL, db.Handle) {
	t.Helper()
	sqlDB := testdbfixture.Open(t, name)

	sessions := store.NewSQL(sqlDB)
	hub := events.NewMemoryHub()
	reg, err := workflowdef.RegistryFromDirs("")
	testutil.FailErr(t, "RegistryFromDirs", err)
	return NewManager(workflowpersistence.New(sqlDB), sessions, reg, &events.Publisher{Hub: hub}), hub, sessions, sqlDB
}

// Transcript rows patch a client's store on arrival and each names its run, so
// an event carrying only an id leaves the span unresolvable for a round trip.
func TestAmbientStartPublishesTheEnrichedRun(t *testing.T) {
	mgr, hub, sessions, sqlDB := newRunManagerForEvents(t, "run-event.db")

	ctx := context.Background()
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, t.TempDir())
	sess, err := sessions.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)

	subCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	ch, unsub, err := hub.Subscribe(subCtx, events.Subscription{Project: sess.ProjectID, Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "subscribe", err)
	defer unsub()

	run, err := mgr.Ambient.StartAmbient(ctx, sess.ID, "implement", "1.0.0")
	testutil.FailErr(t, "StartAmbient", err)

	var event *api.WorkflowEvent
	deadline := time.After(5 * time.Second)
	for event == nil {
		select {
		case env, ok := <-ch:
			if !ok {
				t.Fatal("hub closed before the run transition arrived")
			}
			if env.Topic != api.EventTopicWorkflow {
				continue
			}
			var ev api.WorkflowEvent
			testutil.FailErr(t, "unmarshal workflow event", json.Unmarshal(env.Data, &ev))
			if ev.WorkflowRunID == run.ID {
				event = &ev
			}
		case <-deadline:
			t.Fatalf("timeout waiting for the workflow event for run %s", run.ID)
		}
	}

	if event.Run == nil {
		t.Fatalf("workflow event for run %s carried no run — a client cannot span the transcript without a refetch", run.ID)
	}
	if event.Run.ID != run.ID {
		t.Fatalf("event run id = %q want %q", event.Run.ID, run.ID)
	}
	if event.Run.Status != run.Status {
		t.Fatalf("event run status = %q want %q", event.Run.Status, run.Status)
	}
	if event.Run.Revision != run.Revision {
		t.Fatalf("event run revision = %d want %d — a client orders streamed and refetched state by it",
			event.Run.Revision, run.Revision)
	}
	// Ambient chrome is decided from attach_policy; a stripped copy renders the
	// session's implicit workflow as an explicitly started one.
	if event.Run.AttachPolicy != run.AttachPolicy {
		t.Fatalf("event run attach_policy = %q want %q", event.Run.AttachPolicy, run.AttachPolicy)
	}
	if event.Run.UI == nil {
		t.Fatal("event run has no ui — the stream projection must match what REST returns")
	}
}
