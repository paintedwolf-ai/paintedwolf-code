//go:build integration

package coordinator_test

import (
	"context"
	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/coordinator"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/assembly"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/rules"
	"github.com/lycaon/lycaon/internal/session"
	sessionposture "github.com/lycaon/lycaon/internal/session/posture"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/extpackstest"
	"github.com/lycaon/lycaon/internal/testutil/oartest"
	"github.com/lycaon/lycaon/internal/testutil/prompttest"
	"github.com/lycaon/lycaon/internal/toolhost"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func sessionIntegrationRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Join(filepath.Dir(file), "..", "..")
}

func wireManagerPromptPolicy(t *testing.T, mgr *session.Manager, root string) {
	t.Helper()
	oartest.InstallCloseoutPolicy(t, mgr)
	rt, err := toolhost.NewRuntime(toolhost.RuntimeConfig{ConfigRoot: root, Catalog: extpackstest.StockCatalog(t)})
	testutil.FailErr(t, "toolhost.NewRuntime", err)
	mgr.SetToolInvoker(rt.Executor)
	postures, err := session.LoadPostureRegistry()
	testutil.FailErr(t, "LoadPostureRegistry", err)
	packs, err := rules.LoadBundledRules()
	testutil.FailErr(t, "LoadBundledRules", err)
	if err := rules.ValidatePostureRules(postures, sessionposture.AllSessionPostures(), packs); err != nil {
		testutil.FailErr(t, "ValidatePostureRules", err)
	}
	condReg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{})
	testutil.FailErr(t, "NewDefaultRegistry", err)
	if err := rules.RegisterRuleConditions(condReg); err != nil {
		testutil.FailErr(t, "RegisterRuleConditions", err)
	}
	engine, err := rules.NewPostureRuleEngine(postures, packs, condReg)
	testutil.FailErr(t, "NewPostureRuleEngine", err)
	mgr.SetPostureRegistry(postures)
	mgr.SetRuleEngine(engine)
}

func TestManagerPromptUsesCoordinatorAssembly(t *testing.T) {
	t.Setenv("LYCAON_LLM_MOCK", "1")
	root := sessionIntegrationRoot(t)
	ctx := context.Background()
	rec := llm.NewRecordingClient(llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{{Pattern: ".", Text: "ok"}}}))
	store := store.NewMemory()
	mgr := session.NewManager(store, rec, tools.NewStubRegistry(), settings.DefaultSessionLimits())
	wireManagerPromptPolicy(t, mgr, root)
	mgr.SetPromptEngine(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{ModuleRoot: root}))
	mgr.SetCoordinatorTurnFrameSource(&phaseStubCoordinator{phase: "plan", brief: "Coordinate."})

	sess, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureSpec}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session in store", err)
	if _, err := mgr.Prompt(ctx, sess.ID, "start"); err != nil {
		testutil.FailErr(t, "mgr.Prompt failed", err)
	}
	req := rec.LastRequest()
	if len(req.Messages) == 0 {
		t.Fatal("expected messages")
	}
	if req.Messages[0].Role != api.MessageRoleSystem {
		t.Fatalf("first role = %s", req.Messages[0].Role)
	}
}

func TestManagerKickNudgeViaRuntime(t *testing.T) {
	t.Setenv("LYCAON_LLM_MOCK", "1")
	root := sessionIntegrationRoot(t)
	ctx := context.Background()
	store := store.NewMemory()
	rec := llm.NewRecordingClient(llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{{Pattern: ".", Text: "ok"}}}))
	mgr := session.NewManager(store, rec, tools.NewStubRegistry(), settings.DefaultSessionLimits())
	wireManagerPromptPolicy(t, mgr, root)
	testutil.FailErr(t, "install anchor registry", mgr.InstallAnchorRegistry())
	mgr.SetPromptEngine(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{ModuleRoot: root}))

	sess, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureSpec}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session in store", err)
	mgr.Emit(context.Background(), sess.ID, anchor.ComposeDone, anchor.Envelope{})
	if _, err := mgr.Prompt(ctx, sess.ID, "continue"); err != nil {
		testutil.FailErr(t, "mgr.Prompt failed", err)
	}
	// Host kicks retain their system role and timeline position.
	found := false
	for _, msg := range rec.LastRequest().Messages {
		if msg.Role == api.MessageRoleSystem && strings.Contains(msg.Content, "Compose") {
			found = true
		}
	}
	if !found {
		t.Fatal("expected compose kick as host system message")
	}
}

func TestRuntimeAssemblyEngineDirect(t *testing.T) {
	root := sessionIntegrationRoot(t)
	pe := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{ModuleRoot: root})
	deps := assembly.AssemblyDeps{
		Prompts:          pe,
		Injects:          prompts.NewInjectRenderer(pe),
		Limits:           func(context.Context, *api.Session) settings.SessionLimits { return settings.DefaultSessionLimits() },
		CoordinatorFrame: &phaseStubCoordinator{phase: "implement", brief: "go"},
		PromptToolLister: prompttest.CoordinatorTools,
	}
	rt := coordinator.NewRuntime(coordinator.RuntimeDeps{
		AssemblyDeps: func() assembly.AssemblyDeps { return deps },
	})
	rt.BeginPromptTurn("s1", anchor.InformRender(anchor.GateBlocked))
	msgs, err := rt.BuildCompletionMessages(context.Background(), &api.Session{
		ID: "s1", Posture: api.SessionPostureSpec, WorkspacePath: t.TempDir(),
	}, []api.Message{{Role: api.MessageRoleUser, Content: "hi"}}, nil)
	testutil.FailErr(t, "rt.BuildCompletionMessages failed", err)
	if len(msgs) < 2 {
		t.Fatalf("messages = %d", len(msgs))
	}
}

type phaseStubCoordinator struct {
	phase string
	brief string
}

func (p *phaseStubCoordinator) BuildCoordinatorTurnFrame(_ context.Context, _ string, _ *api.Session) (inject.CoordinatorTurnFrame, error) {
	return inject.CoordinatorTurnFrame{RunContext: api.CoordinatorRunContext{
		WorkflowID: "wf-1", CurrentPhase: p.phase, CoordinatorBrief: p.brief,
	}}, nil
}

var _ inject.CoordinatorTurnFrameSource = (*phaseStubCoordinator)(nil)
