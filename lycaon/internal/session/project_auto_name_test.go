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

func TestAutoNameProjectIdempotentOnReplay(t *testing.T) {
	t.Setenv("LYCAON_LLM_MOCK", "1")
	ctx := context.Background()
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())

	reg := project.NewMemoryRegistry()
	p, err := reg.Create(ctx, project.CreateParams{Draft: true})
	testutil.FailErr(t, "create project", err)

	store := store.NewMemory()
	mock := llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{
		{Pattern: ".*", Text: "ok"},
	}})
	mgr := NewHost(store, Models{Client: mock, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	mgr.SetProjectRegistry(reg)
	hub := events.NewMemoryHub()
	mgr.SetEventPublisher(&events.Publisher{Hub: hub, Lookup: project.ScopeLookup{Registry: reg}})

	sess, err := mgr.Chats.CreateForProject(ctx, p.ID, api.SessionPostureBuild)
	testutil.FailErr(t, "create session", err)

	userText := "Build a CLI todo tracker with SQLite persistence"

	eventsCh, unsubscribe, err := hub.Subscribe(ctx, events.Subscription{Project: p.ID, Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "subscribe", err)
	defer unsubscribe()

	mgr.Chats.Naming.ProjectFromPrompt(ctx, sess, userText)
	mgr.Chats.Naming.ProjectFromPrompt(ctx, sess, userText)

	got, err := reg.Get(ctx, p.ID)
	testutil.FailErr(t, "get project", err)
	want := project.NameProject(ctx, nil, userText)
	if got.Name != want {
		t.Fatalf("name = %q want %q", got.Name, want)
	}

	updated := 0
	deadline := time.After(500 * time.Millisecond)
	for {
		select {
		case envelope := <-eventsCh:
			if envelope.Topic != api.EventTopicProject {
				continue
			}
			var ev api.ProjectEvent
			if err := json.Unmarshal(envelope.Data, &ev); err != nil {
				testutil.FailErr(t, "decode project event", err)
			}
			if ev.Action == api.ProjectEventUpdated {
				updated++
			}
		case <-deadline:
			if updated != 1 {
				t.Fatalf("project.updated count = %d want 1", updated)
			}
			return
		}
	}
}
