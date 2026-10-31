//go:build integration

package session_test

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	coordinatorsurface "github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/coordinator/turnload"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testutil"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestMockLLMReceivesWorkflowFilteredTools(t *testing.T) {
	rec := llm.NewRecordingClient(llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{{Pattern: ".", Text: "ok"}}}))
	specFix := setupContextualToolsFixtureWithLLM(t, api.SessionPostureSpec, rec)
	specManifest := workflowdef.FinalizeManifest(workflowdef.Manifest{
		ID: "spec-tools", Version: "1.0.0", InitialPosture: "spec",
		PhaseDefs: []workflowdef.PhaseDef{{ID: "work", CompleteWhen: workflowdef.CompleteWhenGatesSatisfied, Gates: []string{"research_satisfied"}}},
	})
	specFix.Workflow.Resolver.Overlay = workflowdef.NewRegistry(map[string]workflowdef.Manifest{"spec-tools@1.0.0": specManifest})
	_, err := specFix.Workflow.Starts.StartHuman(t.Context(), specFix.Sess.ID, api.StartWorkflowRunRequest{WorkflowID: specManifest.ID, WorkflowVersion: specManifest.Version})
	testutil.FailErr(t, "start spec workflow", err)
	specFix.Mgr.SetPromptEngine(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{}))
	if _, err := specFix.Mgr.Submissions.Prompt(context.Background(), specFix.Sess.ID, "hello"); err != nil {
		testutil.FailErr(t, "specFix.Mgr.Prompt failed", err)
	}
	specSession, err := specFix.Store.Get(t.Context(), specFix.Sess.ID)
	testutil.FailErr(t, "read spec workflow posture", err)
	if specSession.Posture != api.SessionPostureSpec || !hasTool(rec.LastRequest().Tools, "read") {
		t.Fatalf("spec workflow lost its reading posture: posture=%s tools=%v", specSession.Posture, toolNames(rec.LastRequest().Tools))
	}
	for _, tool := range rec.LastRequest().Tools {
		if tool.Name == "delegate_dispatch" {
			t.Fatalf("spec prompt must not send delegate_dispatch: %v", toolNames(rec.LastRequest().Tools))
		}
	}

	buildRec := llm.NewRecordingClient(llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{{Pattern: ".", Text: "ok"}}}))
	buildFix := setupContextualToolsFixtureWithLLM(t, api.SessionPostureSpec, buildRec)
	buildFix.Mgr.SetPromptEngine(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{}))
	if _, err := buildFix.Mgr.Submissions.Prompt(context.Background(), buildFix.Sess.ID, "hello"); err != nil {
		testutil.FailErr(t, "buildFix.Mgr.Prompt failed", err)
	}
	buildSession, err := buildFix.Store.Get(t.Context(), buildFix.Sess.ID)
	testutil.FailErr(t, "read attached workflow posture", err)
	if buildSession.Posture != api.SessionPostureBuild {
		t.Fatalf("default workflow posture = %s, want build", buildSession.Posture)
	}
	run, err := buildFix.Workflow.Store.Runs.ActiveBySession(t.Context(), buildFix.Sess.ID)
	testutil.FailErr(t, "read attached workflow", err)
	if run == nil || !runstate.IsAmbientRun(run) {
		t.Fatalf("prompt did not attach its default workflow: %+v", run)
	}
	// The investigate surface keeps task loadable: offered, or behind the capability map.
	buildReq := buildRec.LastRequest()
	if !hasTool(buildReq.Tools, "task") && !mapsCapability(buildReq.Messages, "workers, workflows, budgets, and managed secrets") {
		t.Fatalf("build prompt must reach task (ambient implement dispatch primitive): tools=%v", toolNames(buildReq.Tools))
	}
}

func mapsCapability(msgs []api.Message, label string) bool {
	for _, msg := range msgs {
		if msg.Role != api.MessageRoleSystem {
			continue
		}
		at := strings.Index(msg.Content, "### More tools")
		if at >= 0 && strings.Contains(msg.Content[at:], label) {
			return true
		}
	}
	return false
}

func TestMockLLMInvestigateCoordinatorReceivesEndToEndHostContract(t *testing.T) {
	rec := llm.NewRecordingClient(llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{{Pattern: ".", Text: "ok"}}}))
	fix := setupContextualToolsFixtureWithLLM(t, api.SessionPostureBuild, rec)
	investigateEligible := true
	fix.Mgr.SetCoordinatorTurnFrameSource(stubCoordinatorContext{ctx: api.CoordinatorRunContext{
		WorkflowID:                   "implement",
		CurrentPhase:                 "work",
		WorkflowDefaultExecutionMode: coordinatorsurface.ExecutionModeFamilyInvestigate,
		WorkflowInvestigateEligible:  &investigateEligible,
	}})
	recordRequestedLoad(t, fix.Store, fix.Sess.ID, "command")
	fix.Mgr.Coordinator.Loading.SetLedger(turnload.NewLedger())
	fix.Mgr.SetPromptEngine(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{}))
	if _, err := fix.Mgr.Submissions.Prompt(context.Background(), fix.Sess.ID, "run a bounded local-service workflow"); err != nil {
		testutil.FailErr(t, "fix.Mgr.Prompt failed", err)
	}

	req := rec.LastRequest()
	var commandSchema map[string]any
	for _, meta := range req.Tools {
		if meta.Name == "command" {
			commandSchema = meta.ArgsSchema
			break
		}
	}
	if commandSchema == nil {
		t.Fatal("Investigate request must expose command")
	}
	props, ok := commandSchema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("Investigate command schema=%v want properties", commandSchema)
	}
	capability := promptSchemaMap(t, props["capability_request"])
	capabilityProps := promptSchemaMap(t, capability["properties"])
	socketPaths := promptSchemaMap(t, capabilityProps["socket_paths"])
	if socketPaths["type"] != "array" || promptSchemaMap(t, socketPaths["items"])["type"] != "string" {
		t.Fatalf("socket_paths schema=%v", socketPaths)
	}
	if _, ok := capabilityProps["direct_ip"]; !ok {
		t.Fatalf("capability_request properties=%v want direct_ip", capabilityProps)
	}
	envValues := promptSchemaMap(t, promptSchemaMap(t, props["env"])["additionalProperties"])
	if envValues["type"] != "string" {
		t.Fatalf("command env value schema=%v", envValues)
	}

	if _, ok := firstSystemMessage(req.Messages); !ok {
		t.Fatal("Investigate request must include a system prompt")
	}
}

func promptSchemaMap(t *testing.T, raw any) map[string]any {
	t.Helper()
	value, ok := raw.(map[string]any)
	if !ok {
		t.Fatalf("schema value=%T want map[string]any", raw)
	}
	return value
}

func TestPromptLoopParitySpecPosture(t *testing.T) {
	rec := llm.NewRecordingClient(llm.NewMockProvider(&llm.MockConfig{
		Responses: []llm.MockResponseEntry{{Pattern: ".", Text: "ok"}},
	}))
	fix := setupContextualToolsFixtureWithLLM(t, api.SessionPostureSpec, rec)
	ctx := context.Background()
	if _, err := fix.Mgr.Submissions.Prompt(ctx, fix.Sess.ID, "hello"); err != nil {
		testutil.FailErr(t, "fix.Mgr.Prompt failed", err)
	}
	for _, tool := range rec.LastRequest().Tools {
		if tool.Name == "delegate_dispatch" {
			t.Fatalf("spec prompt must omit delegate_dispatch, got %d tools", len(rec.LastRequest().Tools))
		}
	}
}

func TestWorkerAndCoordinatorSharePromptLoop(t *testing.T) {
	fix := setupContextualToolsFixture(t, api.SessionPostureBuild)
	runtime := fix.Mgr.Coordinator.Runtime
	if runtime == nil || fix.Mgr.Runner.Coordinator != runtime {
		t.Fatal("coordinator and turn execution must share one prompt runtime")
	}
	wakeLoop := runtime.CoordinatorLoop()
	coordinatorSnapshot := runtime.PromptLoop()
	coordinatorSnapshot.Context.Deps.SetPromptTurnSurface(fix.Sess.ID, "parent-surface")
	t.Cleanup(func() { runtime.EndPromptTurn(fix.Sess.ID) })
	ctx := context.Background()
	child, err := fix.Mgr.Workers.SpawnChild(ctx, fix.Sess.ID, api.SpawnChildRequest{
		AgentType: orchestration.ProfileImplementer,
		Prompt:    "implement",
	})
	testutil.FailErr(t, "fix.Mgr.SpawnChild failed", err)
	if child.ParentSessionID != fix.Sess.ID {
		t.Fatalf("child parent=%q, want %q", child.ParentSessionID, fix.Sess.ID)
	}
	t.Cleanup(func() { runtime.EndPromptTurn(child.ID) })
	workerSnapshot := fix.Mgr.Runner.Coordinator.PromptLoop()
	if fix.Mgr.Runner.Coordinator != runtime || fix.Mgr.Coordinator.Runtime != runtime || runtime.CoordinatorLoop() != wakeLoop {
		t.Fatal("worker creation and dependency refresh must retain the shared runtime and wake owner")
	}
	if workerSnapshot == coordinatorSnapshot {
		t.Fatal("prompt turns must receive fresh dependency snapshots")
	}
	if got := workerSnapshot.Context.Deps.PromptTurnSurface(fix.Sess.ID); got != "parent-surface" {
		t.Fatalf("refreshed worker snapshot lost shared parent state: %q", got)
	}
	workerSnapshot.Context.Deps.SetPromptTurnSurface(child.ID, "child-surface")
	if got := coordinatorSnapshot.Context.Deps.PromptTurnSurface(child.ID); got != "child-surface" {
		t.Fatalf("coordinator snapshot cannot read shared child state: %q", got)
	}
	if got := coordinatorSnapshot.Context.Deps.PromptTurnSurface(fix.Sess.ID); got != "parent-surface" {
		t.Fatalf("child state replaced the parent turn surface: %q", got)
	}
}

func TestWorkerChildPromptUsesToolPolicy(t *testing.T) {
	fix := setupContextualToolsFixture(t, api.SessionPostureBuild)
	ctx := context.Background()
	parent := fix.Sess
	child, err := fix.Mgr.Workers.SpawnChild(ctx, parent.ID, api.SpawnChildRequest{
		AgentType: orchestration.ProfileImplementer,
		Prompt:    "implement feature",
	})
	testutil.FailErr(t, "fix.Mgr.SpawnChild failed", err)
	profile, err := fix.Mgr.Profiles.ResolvePromptToolProfile(ctx, child.ID)
	testutil.FailErr(t, "fix.Mgr.Profiles.ResolvePromptToolProfile failed", err)
	listed := fix.Mgr.Coordinator.Guards.Policy().ListForPrompt(ctx, child, profile)
	for _, meta := range listed {
		if meta.Name == "delegate_dispatch" {
			t.Fatal("worker child must not list delegate_dispatch")
		}
	}
	names := make([]string, 0, len(listed))
	for _, meta := range listed {
		names = append(names, meta.Name)
	}
	has := func(name string) bool {
		for _, n := range names {
			if n == name {
				return true
			}
		}
		return false
	}
	if !has("write") || !has("edit") || !has("replace_lines") {
		t.Fatalf("implementer child must list write/edit/replace_lines on wire, got %v", names)
	}
}

func TestMockLLMRecoversPersistedToolSchemas(t *testing.T) {
	recorder := llm.NewRecordingClient(llm.NewMockProvider(&llm.MockConfig{
		Responses: []llm.MockResponseEntry{{Pattern: ".", Text: "ready"}},
	}))
	fixture := setupContextualToolsFixtureWithLLM(t, api.SessionPostureBuild, recorder)
	eligible := true
	fixture.Mgr.SetCoordinatorTurnFrameSource(stubCoordinatorContext{ctx: api.CoordinatorRunContext{
		WorkflowID:                   "implement",
		CurrentPhase:                 "work",
		WorkflowDefaultExecutionMode: coordinatorsurface.ExecutionModeFamilyInvestigate,
		WorkflowInvestigateEligible:  &eligible,
	}})
	fixture.Mgr.SetPromptEngine(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{}))
	assistantID, callID := uuid.NewString(), uuid.NewString()
	testutil.FailErr(t, "persist schema activation", fixture.Store.AppendMessages(t.Context(), fixture.Sess.ID,
		api.Message{ID: assistantID, Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{{ID: callID, Name: "request_tools"}}},
		api.Message{ID: uuid.NewString(), Role: api.MessageRoleTool, ToolResult: &api.ToolResult{
			Tool: "request_tools", ToolCallID: callID, AssistantMessageID: assistantID,
			Outcome: api.ToolResultOutcomeCompleted, Content: `{"need":"run the local service","loaded":["command"]}`,
			Invocation: &api.InvocationReceipt{Tool: "request_tools", ToolCallID: callID, Owner: "tool_surface", Status: api.InvocationStatusCompleted, Invoked: true},
		}},
	))
	// request_tools records the standing set it produced.
	_, err := fixture.Store.PutTurnLoadReceipt(t.Context(), store.TurnLoadReceipt{
		SessionID: fixture.Sess.ID, Trigger: store.TurnLoadTriggerRequest, ToolCallID: callID,
		Decisions: "{}", StateJSON: "{}",
		Standing: `{"tools":[{"tool":"command","source":"requested","need":"run the local service"}]}`,
	})
	testutil.FailErr(t, "record request receipt", err)
	// A host restart drops the activation cache while retaining the store.
	fixture.Mgr.Coordinator.Loading.SetLedger(turnload.NewLedger())
	_, err = fixture.Mgr.Submissions.Prompt(t.Context(), fixture.Sess.ID, "continue after restart")
	testutil.FailErr(t, "resume persisted session", err)
	for _, meta := range recorder.LastRequest().Tools {
		if meta.Name == "command" {
			if len(meta.ArgsSchema) == 0 {
				t.Fatal("restored command lacks its current argument schema")
			}
			return
		}
	}
	t.Fatal("first resumed provider request omitted the previously loaded command schema")
}

func TestListedToolsPassRulesEvalWithEmptyArgs(t *testing.T) {
	fix := setupContextualToolsFixture(t, api.SessionPostureBuild)
	ctx := context.Background()
	listed := fix.Mgr.Coordinator.Guards.Policy().ListForPrompt(ctx, fix.Sess, fix.ProfileID)
	for _, meta := range listed {
		if err := fix.Mgr.Coordinator.Guards.Policy().EvaluateInvoke(ctx, fix.Sess, meta.Name, nil); err != nil {
			t.Fatalf("listed tool %q failed rules eval: %v", meta.Name, err)
		}
	}
}

func TestSpecPromptToolCountLessThanBuild(t *testing.T) {
	specFix := setupContextualToolsFixture(t, api.SessionPostureSpec)
	buildFix := setupContextualToolsFixture(t, api.SessionPostureBuild)
	ctx := context.Background()
	specCount := len(specFix.Mgr.Coordinator.Guards.Policy().ListForPrompt(ctx, specFix.Sess, specFix.ProfileID))
	buildCount := len(buildFix.Mgr.Coordinator.Guards.Policy().ListForPrompt(ctx, buildFix.Sess, buildFix.ProfileID))
	if specCount >= buildCount {
		t.Fatalf("spec tool count = %d want < build %d", specCount, buildCount)
	}
}

func TestToolpolicyEngineListInvokeParityEmptyArgs(t *testing.T) {
	fix := setupContextualToolsFixture(t, api.SessionPostureBuild)
	ctx := context.Background()
	listed := fix.Mgr.Coordinator.Guards.Policy().ListForPrompt(ctx, fix.Sess, fix.ProfileID)
	for _, meta := range listed {
		if err := fix.Mgr.Coordinator.Guards.Policy().EvaluateInvoke(ctx, fix.Sess, meta.Name, nil); err != nil {
			t.Fatalf("listed tool %q failed empty-args invoke: %v", meta.Name, err)
		}
	}
}
