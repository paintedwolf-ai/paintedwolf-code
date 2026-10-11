package session

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestSetTitleOverwritesAndPublishes(t *testing.T) {
	t.Setenv("LYCAON_LLM_MOCK", "1")
	ctx := context.Background()
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())

	reg := project.NewMemoryRegistry()
	p, err := reg.Create(ctx, project.CreateParams{Draft: true})
	testutil.FailErr(t, "create project", err)

	mem := store.NewMemory()
	mgr := NewHost(mem, Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	mgr.SetProjectRegistry(reg)
	hub := events.NewMemoryHub()
	mgr.SetEventPublisher(&events.Publisher{Hub: hub, Lookup: project.ScopeLookup{Registry: reg}})

	sess, err := mgr.Chats.CreateForProject(ctx, p.ID, api.SessionPostureBuild)
	testutil.FailErr(t, "create session", err)

	eventsCh, unsubscribe, err := hub.Subscribe(ctx, events.Subscription{Project: p.ID, Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "subscribe", err)
	defer unsubscribe()

	updated, err := mgr.Chats.Naming.SetTitle(ctx, sess.ID, "  Ship readiness checklist  ")
	testutil.FailErr(t, "set title", err)
	if updated.Title != "Ship readiness checklist" {
		t.Fatalf("title = %q", updated.Title)
	}

	got, err := mem.Get(ctx, sess.ID)
	testutil.FailErr(t, "get", err)
	if got.Title != "Ship readiness checklist" {
		t.Fatalf("stored title = %q", got.Title)
	}

	deadline := time.After(500 * time.Millisecond)
	for {
		select {
		case envelope := <-eventsCh:
			if envelope.Topic != api.EventTopicSession {
				continue
			}
			var ev api.SessionEvent
			if err := json.Unmarshal(envelope.Data, &ev); err != nil {
				testutil.FailErr(t, "decode", err)
			}
			if ev.ID == sess.ID && ev.Title == "Ship readiness checklist" {
				return
			}
		case <-deadline:
			t.Fatal("timed out waiting for SessionEvent title")
		}
	}
}

func TestUpdateTitleIfUnsetNoOpsAfterManualSet(t *testing.T) {
	t.Setenv("LYCAON_LLM_MOCK", "1")
	ctx := context.Background()
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())

	reg := project.NewMemoryRegistry()
	p, err := reg.Create(ctx, project.CreateParams{Draft: true})
	testutil.FailErr(t, "create project", err)

	mem := store.NewMemory()
	mock := llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{
		{Pattern: ".*", Text: "ok"},
	}})
	mgr := NewHost(mem, Models{Client: mock, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	mgr.SetProjectRegistry(reg)

	sess, err := mgr.Chats.CreateForProject(ctx, p.ID, api.SessionPostureBuild)
	testutil.FailErr(t, "create session", err)

	_, err = mgr.Chats.Naming.SetTitle(ctx, sess.ID, "Manual name")
	testutil.FailErr(t, "manual set", err)

	// The auto-title path does not overwrite a manual title.
	mgr.Chats.Naming.SessionFromPrompt(ctx, sess, "Build a completely different thing")
	got, err := mem.Get(ctx, sess.ID)
	testutil.FailErr(t, "get", err)
	if got.Title != "Manual name" {
		t.Fatalf("title overwritten to %q", got.Title)
	}

	updated, err := mem.UpdateTitleIfUnset(ctx, sess.ID, "Race loser")
	testutil.FailErr(t, "ifunset", err)
	if updated {
		t.Fatal("UpdateTitleIfUnset reported update after manual set")
	}
}

func TestSetTitleWinsRaceVsIfUnset(t *testing.T) {
	ctx := context.Background()
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())

	reg := project.NewMemoryRegistry()
	p, err := reg.Create(ctx, project.CreateParams{Draft: true})
	testutil.FailErr(t, "create project", err)

	mem := store.NewMemory()
	mgr := NewHost(mem, Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	mgr.SetProjectRegistry(reg)

	sess, err := mgr.Chats.CreateForProject(ctx, p.ID, api.SessionPostureBuild)
	testutil.FailErr(t, "create session", err)

	// Concurrent-style: Set always writes; subsequent IfUnset no-ops.
	_, err = mgr.Chats.Naming.SetTitle(ctx, sess.ID, "User wins")
	testutil.FailErr(t, "set", err)
	ok, err := mem.UpdateTitleIfUnset(ctx, sess.ID, "Auto late")
	testutil.FailErr(t, "ifunset", err)
	if ok {
		t.Fatal("IfUnset should no-op when title already set")
	}
	got, err := mem.Get(ctx, sess.ID)
	testutil.FailErr(t, "get", err)
	if got.Title != "User wins" {
		t.Fatalf("title = %q", got.Title)
	}
}
