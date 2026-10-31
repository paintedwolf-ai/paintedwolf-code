package session_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/agentdef"
	"github.com/lycaon/lycaon/internal/board"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/session"
	sessionlimits "github.com/lycaon/lycaon/internal/session/limits"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/session/workeradmission"
	"github.com/lycaon/lycaon/internal/session/workercloseout"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/testdbseed"
	repotest "github.com/lycaon/lycaon/internal/testsetup/repoinfo"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/oartest"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestPolicyAdvisoryReachesActualModelRequest(t *testing.T) {
	for _, anchor := range []string{oar.AnchorContentInput, oar.AnchorCoordinatorPreInvoke} {
		t.Run(anchor, func(t *testing.T) {
			t.Setenv("LYCAON_LLM_MOCK", "1")
			mem := store.NewMemory()
			root := t.TempDir()
			response := llm.MockResponseEntry{Pattern: ".", Text: "ok"}
			if anchor == oar.AnchorCoordinatorPreInvoke {
				response.ToolCalls = []llm.MockToolCall{{ID: "call-directory", Name: "list_dir", Args: map[string]any{"path": root}}}
				response.FollowUpText = "complete"
			}
			recorder := llm.NewRecordingClient(llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{response}}))
			registry := tools.NewStubRegistry()
			testutil.FailErr(t, "register directory tool", registry.Register("list_dir", func(context.Context, map[string]any, tools.ToolContext) (string, error) {
				return "[]", nil
			}))
			mgr := session.NewHost(mem, session.Models{Client: recorder, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, registry)
			projects := project.NewMemoryRegistry()
			proj, err := project.CreateWithRoot(t.Context(), projects, root)
			testutil.FailErr(t, "create rooted project", err)
			mgr.SetProjectRegistry(projects)
			mgr.SetCoordinatorTurnFrameSource(&phaseStubCoordinator{phase: "expand", surface: "implement_investigate"})
			wirePromptTestManager(t, mgr)
			engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
			mgr.SetPromptEngine(engine)
			guidance.SetGuidanceRenderer(prompts.NewGuidanceRenderer(engine))
			rule := &oar.Rule{OAR: "1.0", ID: "DELIVERY_PROBE", Kind: oar.KindPolicy, Anchor: anchor, Effect: oar.EffectNudge, OnError: "fail_closed", Enforcement: "enforce", Copy: oar.Copy{What: "Carry this boundary observation forward"}}
			pipeline := oar.NewGuardPipeline(oar.NewRuleSet([]*oar.Rule{rule}), nil, oar.NewCounterStore())
			pipeline.EnableAnchor(anchor)
			pipeline.EnableAnchor(oar.AnchorCoordinatorCloseoutCheck)
			mgr.SetOARPipeline(pipeline, oar.NewRenderer(nil, nil))
			sess, err := mem.Create(t.Context(), api.CreateSessionRequest{Posture: api.SessionPostureBuild}, proj.ID)
			testutil.FailErr(t, "create session", err)
			_, err = mgr.Submissions.Prompt(t.Context(), sess.ID, "continue")
			testutil.FailErr(t, "run prompt", err)
			if anchor == oar.AnchorCoordinatorPreInvoke && len(recorder.AllRequests()) < 2 {
				t.Fatal("tool invocation did not reach a second model request")
			}
			found := false
			for _, message := range recorder.LastRequest().Messages {
				if strings.Contains(message.Content, "Code: DELIVERY_PROBE") {
					found = true
				}
			}
			if !found {
				t.Fatalf("%s advisory absent from actual model request", anchor)
			}
			messages, err := mem.GetMessages(t.Context(), sess.ID)
			testutil.FailErr(t, "read transcript", err)
			found = false
			for _, message := range messages {
				if message.Kind == api.MessageKindCoordinatorGuidance && strings.Contains(message.Content, "Code: DELIVERY_PROBE") {
					found = true
					if len(message.ContentParts) != 2 || message.ContentParts[1].Authority != api.ContentAuthorityNone {
						t.Fatalf("feedback facts gained instruction authority: %#v", message.ContentParts)
					}
				}
			}
			if !found {
				t.Fatal("model feedback was not persisted")
			}
		})
	}
}

func TestWorkerFinalizeUsesJobScopedTranscriptRead(t *testing.T) {
	resolver := &stubSummaryResolver{msgs: []api.Message{completeLegToolRow(map[string]any{"leg_status": "complete", "brief": "done"})}}
	outcome, workerEvalErr := workercloseout.FinalizeWorkerSummaryForChild(
		context.Background(), resolver, resolver, "reused-child", "survey",
		workercloseout.WorkerSummaryFinalizeOpts{WorkerJobID: "job-current"},
	)
	testutil.FailErr(t, "evaluate worker completion", workerEvalErr)
	if outcome.Status != string(api.WorkerSummaryStatusComplete) {
		t.Fatalf("status = %q want complete", outcome.Status)
	}
	if resolver.workerJobTranscriptReads == 0 {
		t.Fatal("worker finalization did not use the job-scoped transcript query")
	}
	if resolver.fullTranscriptReads != 0 {
		t.Fatalf("full transcript reads = %d want 0", resolver.fullTranscriptReads)
	}
}

func loadTestAgentRegistry(t *testing.T) *orchestration.MemoryAgentRegistry {
	t.Helper()
	reg := orchestration.NewMemoryAgentRegistry()
	if err := orchestration.LoadRequiredAgentRegistry(context.Background(), reg); err != nil {
		testutil.FailErr(t, "LoadRequiredAgentRegistry", err)
	}
	return reg
}

func newPromptTestManager(t *testing.T, llmClient modelcall.LLMClient) (*session.Host, *store.Memory) {
	t.Helper()
	t.Setenv("LYCAON_LLM_MOCK", "1")
	store := store.NewMemory()
	toolReg := tools.NewExecutorRegistry(nil, tools.NewDefaultRegistry())
	mgr := session.NewHost(store, session.Models{Client: llmClient, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, toolReg)
	oartest.InstallCloseoutPolicy(t, mgr)
	mgr.Profiles.SetAgentRegistry(loadTestAgentRegistry(t))
	wirePromptTestManager(t, mgr)
	mgr.SetPromptEngine(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{}))
	return mgr, store
}

func firstSystemMessage(msgs []api.Message) (api.Message, bool) {
	for _, msg := range msgs {
		if msg.Role == api.MessageRoleSystem {
			return msg, true
		}
	}
	return api.Message{}, false
}

func TestCompleteStreamInjectsAgentSystemPrompt(t *testing.T) {
	ctx := context.Background()
	inner := llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{{
		Pattern: ".",
		Text:    "ok",
	}}})
	rec := llm.NewRecordingClient(inner)
	mgr, store := newPromptTestManager(t, rec)

	coord, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session in store", err)
	if _, err := mgr.Submissions.Prompt(ctx, coord.ID, "hello coordinator"); err != nil {
		testutil.FailErr(t, "mgr.Submissions.Prompt failed", err)
	}
	coordReq := rec.LastRequest()
	coordSys, ok := firstSystemMessage(coordReq.Messages)
	if !ok {
		t.Fatal("expected system message for coordinator")
	}
	if !strings.Contains(coordSys.Content, "## Investigate") {
		t.Fatalf("coordinator system = %q want investigate default", coordSys.Content)
	}

	child, err := mgr.Workers.SpawnChild(ctx, coord.ID, api.SpawnChildRequest{
		AgentType: orchestration.ProfileImplementer,
		Prompt:    "implement feature",
	})
	testutil.FailErr(t, "mgr.Workers.SpawnChild failed", err)
	if _, err := mgr.Submissions.Prompt(ctx, child.ID, "implement feature"); err != nil {
		testutil.FailErr(t, "mgr.Submissions.Prompt failed", err)
	}
	implReq := rec.LastRequest()
	implSys, ok := firstSystemMessage(implReq.Messages)
	if !ok {
		t.Fatal("expected system message for implementer")
	}
	if !strings.Contains(implSys.Content, "Implementer") {
		t.Fatalf("implementer system = %q want persona render with contract delta", implSys.Content)
	}
	if coordSys.Content == implSys.Content {
		t.Fatal("coordinator and implementer system prompts must differ")
	}
}

func TestCoordinatorPromptIncludesPackBoardInject(t *testing.T) {
	t.Setenv("LYCAON_LLM_MOCK", "1")
	ctx := context.Background()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module coordboard\n"), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	rec := llm.NewRecordingClient(llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{{Pattern: ".", Text: "ok"}}}))
	store := store.NewMemory()
	mgr := session.NewHost(store, session.Models{Client: rec, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	oartest.InstallCloseoutPolicy(t, mgr)
	wirePromptTestManager(t, mgr)
	mgr.SetPromptEngine(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{}))
	mgr.Coordinator.ConfigureBoard(
		&board.InjectBuilder{SnapshotBuilder: &board.SnapshotBuilder{Repo: repotest.NewProvider(t)}},
		board.DefaultInjectFormatter(),
		mgr.Promotion,
	)
	mgr.SetCoordinatorTurnFrameSource(planBoardContextStub{
		ctx: api.CoordinatorRunContext{WorkflowID: "plan", CurrentPhase: "stub"},
	})

	sess, err := store.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session in store", err)
	testdbseed.BindSessionWorkspace(t, store, sess.ID, dir)
	if _, err := mgr.Submissions.Prompt(ctx, sess.ID, "plan"); err != nil {
		testutil.FailErr(t, "mgr.Submissions.Prompt failed", err)
	}
	found := false
	for _, msg := range rec.LastRequest().Messages {
		if msg.Role == api.MessageRoleSystem && strings.Contains(msg.Content, "pack-board:v1") {
			found = true
		}
	}
	if !found {
		t.Fatal("expected pack board sentinel in coordinator messages")
	}
}

func TestCompleteStreamCoordinatorDefault(t *testing.T) {
	ctx := context.Background()
	rec := llm.NewRecordingClient(llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{{
		Pattern: ".",
		Text:    "ok",
	}}}))
	mgr, store := newPromptTestManager(t, rec)

	sess, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureSpec}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session in store", err)
	if _, err := mgr.Submissions.Prompt(ctx, sess.ID, "plan something"); err != nil {
		testutil.FailErr(t, "mgr.Submissions.Prompt failed", err)
	}
	sys, ok := firstSystemMessage(rec.LastRequest().Messages)
	if !ok {
		t.Fatal("expected default coordinator system message")
	}
	if !strings.Contains(sys.Content, "## Investigate") {
		t.Fatalf("system = %q want investigate default", sys.Content)
	}
}

func TestCompleteStreamMissingTemplateFails(t *testing.T) {
	ctx := context.Background()
	mgr, store := newPromptTestManager(t, llm.NewMockProvider(nil))
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	mgr.SetPromptEngine(engine)
	mgr.Profiles.SetAgentRegistry(&stubAgentResolver{profile: agentdef.Profile{
		ID:                   "broken",
		SystemPromptTemplate: "agents/missing.md",
	}})

	sess, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session in store", err)
	_ = mgr.Chats.SetAgentType(ctx, sess.ID, "broken")
	if _, err := mgr.Submissions.Prompt(ctx, sess.ID, "hi"); err == nil {
		t.Fatal("expected prompt error for missing template")
	} else if !strings.Contains(err.Error(), "system prompt") {
		t.Fatalf("error = %v", err)
	}
}

type stubAgentResolver struct {
	profile agentdef.Profile
}

func (s *stubAgentResolver) Get(id string) (agentdef.Profile, error) {
	if id == s.profile.ID {
		return s.profile, nil
	}
	return agentdef.Profile{}, fmt.Errorf("agent profile %q not found", id)
}

type planBoardContextStub struct {
	ctx api.CoordinatorRunContext
}

func (s planBoardContextStub) BuildCoordinatorTurnFrame(_ context.Context, _ string, _ *api.Session) (inject.CoordinatorTurnFrame, error) {
	return inject.CoordinatorTurnFrame{RunContext: s.ctx}, nil
}

func TestParseTaskMaxToolLoopsFromArgs(t *testing.T) {
	n, err := workeradmission.ParseTaskMaxToolLoopsFromArgs(map[string]any{"max_tool_loops": float64(5)})
	testutil.FailErr(t, "workeradmission.ParseTaskMaxToolLoopsFromArgs", err)
	if n != 5 {
		t.Fatalf("max_tool_loops = %d want 5", n)
	}
	if _, err := workeradmission.ParseTaskMaxToolLoopsFromArgs(map[string]any{"max_tool_loops": float64(1.5)}); err == nil {
		t.Fatal("expected error for non-integer max_tool_loops")
	}
}

func TestValidateTaskMaxToolLoopsCode(t *testing.T) {
	budget := spawn.DefaultWorkerToolBudget()
	if code := workeradmission.ValidateTaskMaxToolLoopsCode(0, budget); code != "" {
		t.Fatalf("zero = %q want empty", code)
	}
	if code := workeradmission.ValidateTaskMaxToolLoopsCode(budget.Min, budget); code != "" {
		t.Fatalf("min valid = %q", code)
	}
	if code := workeradmission.ValidateTaskMaxToolLoopsCode(budget.Max, budget); code != "" {
		t.Fatalf("max valid = %q", code)
	}
	if code := workeradmission.ValidateTaskMaxToolLoopsCode(budget.Min-1, budget); code != workeradmission.TaskMaxToolLoopsInvalidCode {
		t.Fatalf("below min = %q want %s", code, workeradmission.TaskMaxToolLoopsInvalidCode)
	}
	if code := workeradmission.ValidateTaskMaxToolLoopsCode(budget.Max+1, budget); code != workeradmission.TaskMaxToolLoopsInvalidCode {
		t.Fatalf("above max = %q want %s", code, workeradmission.TaskMaxToolLoopsInvalidCode)
	}
}

func TestApplyWorkerMaxToolLoopsUsesHostDefault(t *testing.T) {
	sess := &api.Session{ParentSessionID: "parent-1", AgentType: "path-explorer"}
	lim := sessionlimits.ApplyWorkerMaxToolLoops(settings.DefaultSessionLimits(), sess)
	if lim.MaxIterations != spawn.DefaultWorkerMaxToolLoops {
		t.Fatalf("max_iterations = %d want default %d", lim.MaxIterations, spawn.DefaultWorkerMaxToolLoops)
	}
}

func TestApplyWorkerMaxToolLoopsSessionOverride(t *testing.T) {
	sess := &api.Session{ParentSessionID: "parent-1", AgentType: "path-explorer", MaxToolLoops: 5}
	lim := sessionlimits.ApplyWorkerMaxToolLoops(settings.DefaultSessionLimits(), sess)
	if lim.MaxIterations != 5 {
		t.Fatalf("max_iterations = %d want session override 5", lim.MaxIterations)
	}
}

func TestApplyWorkerMaxToolLoopsSkipsCoordinatorRoot(t *testing.T) {
	sess := &api.Session{AgentType: "coordinator"}
	lim := sessionlimits.ApplyWorkerMaxToolLoops(settings.SessionLimits{MaxIterations: 10}, sess)
	if lim.MaxIterations != 10 {
		t.Fatalf("coordinator max_iterations = %d want unchanged 10", lim.MaxIterations)
	}
}

func TestSetWorkerMaxToolLoopsOnResumeChild(t *testing.T) {
	ctx := context.Background()
	store := store.NewMemory()
	mgr := session.NewHost(store, session.Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, nil)
	parent, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create parent", err)
	child, err := mgr.Workers.SpawnChild(ctx, parent.ID, api.SpawnChildRequest{AgentType: "path-explorer"})
	testutil.FailErr(t, "SpawnChild", err)
	if err := mgr.Workers.SetWorkerMaxToolLoops(ctx, child.ID, 8); err != nil {
		t.Fatalf("SetWorkerMaxToolLoops: %v", err)
	}
	loaded, err := store.Get(ctx, child.ID)
	testutil.FailErr(t, "Get child", err)
	if loaded.MaxToolLoops != 8 {
		t.Fatalf("max_tool_loops = %d want 8", loaded.MaxToolLoops)
	}
}
