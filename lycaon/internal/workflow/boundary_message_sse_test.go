package workflow

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestStartBoundaryMessagesPublishMessageSSE(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "boundary-sse.db")

	store := store.NewSQL(sqlDB)
	hub := events.NewMemoryHub()
	pub := &events.Publisher{Hub: hub}
	reg, err := workflowdef.RegistryFromDirs("")
	testutil.FailErr(t, "RegistryFromDirs", err)
	mgr := NewManager(NewSQLStore(sqlDB), store, reg, pub)

	ctx := context.Background()
	dir := t.TempDir()

	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, dir)

	sess, err := store.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session failed", err)

	subCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	ch, unsub, err := hub.Subscribe(subCtx, events.Subscription{Project: sess.ProjectID, Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "subscribe failed", err)
	defer unsub()

	run := &api.WorkflowRun{
		ID:              "run-1",
		SessionID:       sess.ID,
		ProjectID:       sess.ProjectID,
		WorkflowID:      "plan",
		WorkflowVersion: "1.0.0",
		Status:          api.WorkflowRunStatusRunning,
		CurrentPhase:    "stub",
	}
	if err := mgr.Store.CreateState(ctx, run, "", nil); err != nil {
		testutil.FailErr(t, "create run failed", err)
	}
	messages, _ := startBoundaryMessages(run, "/plan", "11111111-1111-4111-8111-111111111111")
	if err := mgr.appendSessionMessages(ctx, sess.ID, messages...); err != nil {
		testutil.FailErr(t, "append start messages failed", err)
	}

	var appendCount int
	deadline := time.After(2 * time.Second)
	for appendCount < 2 {
		select {
		case env, ok := <-ch:
			if !ok {
				t.Fatal("hub closed")
			}
			if env.Topic != api.EventTopicMessage {
				continue
			}
			var ev api.MessageEvent
			if err := json.Unmarshal(env.Data, &ev); err != nil {
				testutil.FailErr(t, "unmarshal message event", err)
			}
			if ev.SessionID != sess.ID || ev.Op != api.MessageChangeAppend {
				t.Fatalf("event = %+v", ev)
			}
			if ev.Message.Ord <= 0 {
				t.Fatalf("boundary SSE ord = %d want > 0 (stamped copy must ride PublishMessageAppend)", ev.Message.Ord)
			}
			appendCount++
		case <-deadline:
			t.Fatalf("timeout waiting for boundary message SSE; got %d appends", appendCount)
		}
	}
}
