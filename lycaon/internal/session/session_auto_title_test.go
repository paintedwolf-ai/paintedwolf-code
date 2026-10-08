package session

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	ollamaprovider "github.com/lycaon/lycaon/internal/llm/providers/ollama"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestAutoTitleSessionIdempotentOnReplay(t *testing.T) {
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
	mgr := NewManager(store, mock, tools.NewStubRegistry(), settings.DefaultSessionLimits())
	mgr.SetProjectRegistry(reg)
	hub := events.NewMemoryHub()
	mgr.SetEventPublisher(&events.Publisher{Hub: hub, Lookup: project.ScopeLookup{Registry: reg}})

	sess, err := mgr.CreateForProject(ctx, p.ID, api.SessionPostureBuild)
	testutil.FailErr(t, "create session", err)

	userText := "Build a CLI todo tracker with SQLite persistence"

	eventsCh, unsubscribe, err := hub.Subscribe(ctx, events.Subscription{Project: p.ID, Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "subscribe", err)
	defer unsubscribe()

	mgr.autoTitleSessionFromPrompt(ctx, sess, userText)
	mgr.autoTitleSessionFromPrompt(ctx, sess, userText)

	got, err := store.Get(ctx, sess.ID)
	testutil.FailErr(t, "get session", err)
	want := NameSession(ctx, nil, userText)
	if got.Title != want {
		t.Fatalf("title = %q want %q", got.Title, want)
	}

	updated := 0
	deadline := time.After(500 * time.Millisecond)
	for {
		select {
		case envelope := <-eventsCh:
			if envelope.Topic != api.EventTopicSession {
				continue
			}
			var ev api.SessionEvent
			if err := json.Unmarshal(envelope.Data, &ev); err != nil {
				testutil.FailErr(t, "decode session event", err)
			}
			if ev.Title != "" {
				updated++
			}
		case <-deadline:
			if updated != 1 {
				t.Fatalf("session title event count = %d want 1", updated)
			}
			return
		}
	}
}

func TestPublishSessionTitleUsesStoreStatus(t *testing.T) {
	t.Setenv("LYCAON_LLM_MOCK", "1")
	ctx := context.Background()
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())

	reg := project.NewMemoryRegistry()
	p, err := reg.Create(ctx, project.CreateParams{Draft: true})
	testutil.FailErr(t, "create project", err)

	store := store.NewMemory()
	mgr := NewManager(store, nil, tools.NewStubRegistry(), settings.DefaultSessionLimits())
	mgr.SetProjectRegistry(reg)
	hub := events.NewMemoryHub()
	mgr.SetEventPublisher(&events.Publisher{Hub: hub, Lookup: project.ScopeLookup{Registry: reg}})

	sess, err := mgr.CreateForProject(ctx, p.ID, api.SessionPostureBuild)
	testutil.FailErr(t, "create session", err)
	if err := store.SetSessionStatus(ctx, sess.ID, api.SessionStatusBusy); err != nil {
		testutil.FailErr(t, "set session busy", err)
	}

	eventsCh, unsubscribe, err := hub.Subscribe(ctx, events.Subscription{Project: p.ID, Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "subscribe", err)
	defer unsubscribe()

	stale := *sess
	stale.Status = api.SessionStatusIdle
	mgr.publishSessionTitleUpdated(ctx, &stale, "Chess game")

	deadline := time.After(500 * time.Millisecond)
	for {
		select {
		case envelope := <-eventsCh:
			if envelope.Topic != api.EventTopicSession {
				continue
			}
			var ev api.SessionEvent
			if err := json.Unmarshal(envelope.Data, &ev); err != nil {
				testutil.FailErr(t, "decode session event", err)
			}
			if ev.Title != "Chess game" {
				continue
			}
			if ev.Status != api.SessionStatusBusy {
				t.Fatalf("title event status = %q want busy", ev.Status)
			}
			return
		case <-deadline:
			t.Fatal("timeout waiting for session title event")
		}
	}
}

func TestAutoNameProjectSkipsNonDraft(t *testing.T) {
	t.Setenv("LYCAON_LLM_MOCK", "1")
	ctx := context.Background()
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())

	reg := project.NewMemoryRegistry()
	p, err := reg.Create(ctx, project.CreateParams{
		Roots: []project.AttachRootParams{{Path: t.TempDir()}},
	})
	testutil.FailErr(t, "create project", err)
	if stringsTrim := p.Name; stringsTrim == "" {
		t.Fatal("expected folder-backed project to have a name")
	}

	store := store.NewMemory()
	mock := llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{
		{Pattern: ".*", Text: "ignored"},
	}})
	mgr := NewManager(store, mock, tools.NewStubRegistry(), settings.DefaultSessionLimits())
	mgr.SetProjectRegistry(reg)

	sess, err := mgr.CreateForProject(ctx, p.ID, api.SessionPostureBuild)
	testutil.FailErr(t, "create session", err)

	mgr.autoNameProjectFromPrompt(ctx, sess, "Rename me from prompt")

	got, err := reg.Get(ctx, p.ID)
	testutil.FailErr(t, "get project", err)
	if got.Name != p.Name {
		t.Fatalf("project name = %q want %q (prompt must not rename non-draft)", got.Name, p.Name)
	}
}

type blockingNamingLLM struct {
	entered chan struct{}
	release chan struct{}
	text    string
}

func (b *blockingNamingLLM) Complete(ctx context.Context, _ modelcall.CompletionRequest) (*modelcall.Completion, error) {
	select {
	case b.entered <- struct{}{}:
	default:
	}
	select {
	case <-b.release:
		return &modelcall.Completion{Content: b.text}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (b *blockingNamingLLM) Stream(context.Context, modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	return nil, nil
}

func (*blockingNamingLLM) ID() string { return "naming" }

func (*blockingNamingLLM) Models() []modelcall.ModelInfo {
	return []modelcall.ModelInfo{{ID: "naming-model"}}
}

func (*blockingNamingLLM) Profile() providerprofile.Profile { return providerprofile.Profile{} }

func TestKickPromptCurationDoesNotBlockOnNaming(t *testing.T) {
	t.Setenv("LYCAON_LLM_MOCK", "")
	ctx := context.Background()
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())

	reg := project.NewMemoryRegistry()
	p, err := reg.Create(ctx, project.CreateParams{Draft: true})
	testutil.FailErr(t, "create project", err)

	mem := store.NewMemory()
	blocker := &blockingNamingLLM{
		entered: make(chan struct{}, 2),
		release: make(chan struct{}),
		text:    "Background title",
	}
	mgr := NewManager(mem, blocker, tools.NewStubRegistry(), settings.DefaultSessionLimits())
	mgr.SetProjectRegistry(reg)
	providers := &llm.Registry{}
	testutil.FailErr(t, "register naming provider", providers.Register(blocker))
	mgr.llmSvc = &llm.Service{
		Registry: providers,
		Policy: llm.NewInMemoryPolicyStore(llm.ModelPolicy{
			Lite: llm.ModelRef{ProviderID: blocker.ID(), Model: "naming-model"},
		}),
		Utility: llm.NewUtilityPlane(),
	}

	sess, err := mgr.CreateForProject(ctx, p.ID, api.SessionPostureBuild)
	testutil.FailErr(t, "create session", err)

	returned := make(chan struct{})
	go func() {
		mgr.kickPromptCuration(ctx, sess, "Build a CLI todo tracker with SQLite persistence")
		close(returned)
	}()

	select {
	case <-returned:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("kickPromptCuration blocked on naming LLM")
	}

	select {
	case <-blocker.entered:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("naming LLM never started")
	}

	got, err := mem.Get(ctx, sess.ID)
	testutil.FailErr(t, "get session mid-name", err)
	if got.Title != "" {
		t.Fatalf("title set before naming finished: %q", got.Title)
	}

	close(blocker.release)
	mgr.WaitForPromptCuration()

	got, err = mem.Get(ctx, sess.ID)
	testutil.FailErr(t, "get session after name", err)
	if got.Title != "Background title" {
		t.Fatalf("title = %q want %q", got.Title, "Background title")
	}
}

func TestCurationSharesLocalCoordinator(t *testing.T) {
	ctx := context.Background()
	registry := &llm.Registry{}
	testutil.FailErr(t, "register ollama", registry.Register(ollamaprovider.New("ollama-1", "http://localhost:11434/v1", "", nil)))
	policy := llm.NewInMemoryPolicyStore(llm.ModelPolicy{
		Coordinator: llm.ModelRef{ProviderID: "ollama-1", Model: "gemma4:e2b"},
		Lite:        llm.ModelRef{ProviderID: "ollama-1", Model: "gemma4:e2b"},
	})
	mgr := NewManager(store.NewMemory(), nil, tools.NewStubRegistry(), settings.DefaultSessionLimits())
	mgr.llmSvc = &llm.Service{Registry: registry, Policy: policy}

	if !mgr.curationSharesLocalCoordinator(ctx, &api.Session{ProviderID: "ollama-1"}) {
		t.Fatal("shared single-flight coordinator and lite must defer curation")
	}
	if mgr.curationSharesLocalCoordinator(ctx, &api.Session{ProviderID: "together-ai-1"}) {
		t.Fatal("a hosted session override must not defer curation")
	}
	mgr.llmSvc = nil
	if mgr.curationSharesLocalCoordinator(ctx, &api.Session{ProviderID: "ollama-1"}) {
		t.Fatal("no service must not defer curation")
	}
}

func TestWorkflowRequestCurationPreservesManualTitlesAndExcludesWorkers(t *testing.T) {
	t.Setenv("LYCAON_LLM_MOCK", "1")
	mem := store.NewMemory()
	mgr := NewManager(mem, nil, tools.NewStubRegistry(), settings.DefaultSessionLimits())
	parent, err := mem.Create(t.Context(), api.CreateSessionRequest{Posture: api.SessionPostureBuild}, "project-1")
	testutil.FailErr(t, "create root chat", err)
	_, err = mgr.SetTitle(t.Context(), parent.ID, "Manual orchard title")
	testutil.FailErr(t, "set manual title", err)
	child, err := mem.CreateChild(t.Context(), parent, api.SpawnChildRequest{AgentType: "researcher"})
	testutil.FailErr(t, "create worker", err)
	before, err := mem.Get(t.Context(), child.ID)
	testutil.FailErr(t, "get worker title", err)
	mgr.CurateAcceptedWorkflowRequest(t.Context(), parent.ID, "A different workflow request")
	mgr.CurateAcceptedWorkflowRequest(t.Context(), child.ID, "A worker request")
	mgr.CurateAcceptedWorkflowRequest(t.Context(), "missing-session", "A missing request")
	mgr.WaitForPromptCuration()
	got, err := mem.Get(t.Context(), parent.ID)
	testutil.FailErr(t, "get root title", err)
	if got.Title != "Manual orchard title" {
		t.Fatalf("manual title changed to %q", got.Title)
	}
	worker, err := mem.Get(t.Context(), child.ID)
	testutil.FailErr(t, "get worker after curation", err)
	if worker.Title != before.Title {
		t.Fatalf("worker title changed from %q to %q", before.Title, worker.Title)
	}
}

func TestAcceptedWorkflowRequestCurationSurvivesInitiatingTurnCancellation(t *testing.T) {
	t.Setenv("LYCAON_LLM_MOCK", "1")
	mem := store.NewMemory()
	mgr := NewManager(mem, nil, tools.NewStubRegistry(), settings.DefaultSessionLimits())
	sess, err := mem.Create(t.Context(), api.CreateSessionRequest{Posture: api.SessionPostureBuild}, "project-1")
	testutil.FailErr(t, "create chat", err)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	text := "Review the orchard irrigation choices"
	mgr.CurateAcceptedWorkflowRequest(ctx, sess.ID, text)
	mgr.WaitForPromptCuration()
	got, err := mem.Get(t.Context(), sess.ID)
	testutil.FailErr(t, "get named chat", err)
	if got.Title != NameSession(t.Context(), nil, text) {
		t.Fatalf("accepted workflow title = %q", got.Title)
	}
}

func TestEngineShutdownCancelsAndDrainsDetachedNaming(t *testing.T) {
	t.Setenv("LYCAON_LLM_MOCK", "")
	ctx := t.Context()
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	reg := project.NewMemoryRegistry()
	p, err := reg.Create(ctx, project.CreateParams{Draft: true})
	testutil.FailErr(t, "create project", err)
	mem := store.NewMemory()
	blocker := &blockingNamingLLM{entered: make(chan struct{}, 2), release: make(chan struct{}), text: "Should never publish"}
	mgr := NewManager(mem, blocker, tools.NewStubRegistry(), settings.DefaultSessionLimits())
	mgr.SetProjectRegistry(reg)
	providers := &llm.Registry{}
	testutil.FailErr(t, "register naming provider", providers.Register(blocker))
	mgr.llmSvc = &llm.Service{Registry: providers, Policy: llm.NewInMemoryPolicyStore(llm.ModelPolicy{Lite: llm.ModelRef{ProviderID: blocker.ID(), Model: "naming-model"}}), Utility: llm.NewUtilityPlane()}
	sess, err := mgr.CreateForProject(ctx, p.ID, api.SessionPostureBuild)
	testutil.FailErr(t, "create session", err)
	mgr.kickPromptCuration(ctx, sess, "Build a CLI todo tracker with SQLite persistence")
	select {
	case <-blocker.entered:
	case <-time.After(time.Second):
		t.Fatal("detached naming did not start")
	}
	mgr.BeginEngineShutdown()
	testutil.FailErr(t, "drain engine naming", mgr.WaitForEngineShutdown(ctx))
	got, err := mem.Get(ctx, sess.ID)
	testutil.FailErr(t, "read settled session", err)
	if got.Title != "" {
		t.Fatalf("cancelled naming published title %q", got.Title)
	}
	mgr.kickPromptCuration(ctx, sess, "Another request after shutdown")
	select {
	case <-blocker.entered:
		t.Fatal("naming admitted after shutdown")
	default:
	}
}
