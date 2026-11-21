package chats

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/session/promptstate"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestSetModePublishesRootLifecycleAndRejectsWorkerChanges(t *testing.T) {
	ctx := t.Context()
	sessions := store.NewMemory()
	ask := api.SessionModeAsk
	root, err := sessions.Create(ctx, api.CreateSessionRequest{Mode: &ask}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create Ask chat", err)
	worker, err := sessions.CreateChild(ctx, root, api.SpawnChildRequest{AgentType: "repo-researcher"})
	testutil.FailErr(t, "create worker chat", err)
	_, err = sessions.BeginTurn(ctx, store.TurnStart{SessionID: root.ID, ProjectID: root.ProjectID, Origin: store.TurnOriginUser})
	testutil.FailErr(t, "begin Ask turn", err)

	hub := events.NewMemoryHub()
	stream, unsubscribe, err := hub.Subscribe(ctx, events.Subscription{Project: root.ProjectID, Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "subscribe to chat updates", err)
	defer unsubscribe()
	service := New(sessions, nil, nil, nil, nil, Lifecycle{})
	service.SetPublisher(&events.Publisher{Hub: hub})

	if _, err := service.SetMode(ctx, worker.ID, api.SessionModeAgent); !errors.Is(err, ErrWorkerChildLifecycle) {
		t.Fatalf("worker mode change = %v, want %v", err, ErrWorkerChildLifecycle)
	}
	updated, err := service.SetMode(ctx, " "+root.ID+" ", api.SessionModeAgent)
	testutil.FailErr(t, "upgrade chat to Agent", err)
	if updated.Mode != api.SessionModeAgent || !updated.Started {
		t.Fatalf("updated chat mode = %q, started = %v", updated.Mode, updated.Started)
	}
	if _, err := service.SetMode(ctx, root.ID, ask); !errors.Is(err, store.ErrSessionModeLocked) {
		t.Fatalf("started Agent chat change = %v, want %v", err, store.ErrSessionModeLocked)
	}
	hub.FlushDebounced()
	envelope := testutil.Receive(t, "chat mode update", stream)
	var event api.SessionEvent
	testutil.FailErr(t, "decode chat mode update", json.Unmarshal(envelope.Data, &event))
	if envelope.Topic != api.EventTopicSession || event.ID != root.ID || event.Action != api.SessionEventActionUpdated ||
		event.Mode != api.SessionModeAgent || event.Started == nil || !*event.Started {
		t.Fatalf("unexpected chat mode update on %q: %+v", envelope.Topic, event)
	}
}

func TestDeleteKeepsHealthyWorkerChatsAndAllowsDamagedWorkerCleanup(t *testing.T) {
	ctx := t.Context()
	database := testdbfixture.Open(t, "chats.db")
	testdbseed.InsertSession(t, database, "coordinator", testdbseed.DefaultProjectID)
	testdbseed.InsertSession(t, database, "unrelated", testdbseed.DefaultProjectID)
	testdbseed.InsertChildSession(t, database, "worker", "coordinator")
	sessions := store.NewSQL(database)
	service := New(sessions, nil, &promptstate.MutexRegistry{}, nil, nil, Lifecycle{})

	if err := service.Delete(ctx, "worker"); !errors.Is(err, ErrWorkerChildLifecycle) {
		t.Fatalf("healthy worker deletion = %v, want %v", err, ErrWorkerChildLifecycle)
	}
	_, err := database.ExecContext(ctx, "DROP TRIGGER sessions_lineage_immutable")
	testutil.FailErr(t, "allow damaged worker fixture", err)
	_, err = database.ExecContext(ctx, "UPDATE sessions SET root_session_id = 'unrelated' WHERE id = 'worker'")
	testutil.FailErr(t, "damage worker lineage", err)

	testutil.FailErr(t, "delete damaged worker chat", service.Delete(ctx, "worker"))
	if _, err := sessions.Get(ctx, "worker"); !errors.Is(err, store.ErrSessionNotFound) {
		t.Fatalf("deleted worker lookup = %v, want %v", err, store.ErrSessionNotFound)
	}
	for _, root := range []string{"coordinator", "unrelated"} {
		_, err := sessions.Get(ctx, root)
		testutil.FailErr(t, "retain root chat "+root, err)
	}
}
