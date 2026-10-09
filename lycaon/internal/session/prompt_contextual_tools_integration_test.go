//go:build integration

package session_test

import (
	"context"
	"strings"
	"testing"

	coordinatorsurface "github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/coordinator/turnload"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/testutil"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestMockLLMReceivesWorkflowFilteredTools(t *testing.T) {
	rec := llm.NewRecordingClient(llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{{Pattern: ".", Text: "ok"}}}))
	specFix := setupContextualToolsFixtureWithLLM(t, api.SessionPostureSpec, rec)
	specManifest := workflowdef.FinalizeManifest(workflowdef.Manifest{
		ID: "spec-tools", Version: "1.0.0", InitialPosture: "spec",
		PhaseDefs: []workflowdef.PhaseDef{{ID: "work", CompleteWhen: workflowdef.CompleteWhenGatesSatisfied, Gates: []string{"research_satisfied"}}},
	})
	specFix.Workflow.Manifests = workflowdef.NewRegistry(map[string]workflowdef.Manifest{"spec-tools@1.0.0": specManifest})
	_, err := specFix.Workflow.StartHuman(t.Context(), specFix.Sess.ID, api.StartWorkflowRunRequest{WorkflowID: specManifest.ID, WorkflowVersion: specManifest.Version})
	testutil.FailErr(t, "start spec workflow", err)
	specFix.Mgr.SetPromptEngine(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{}))
	if _, err := specFix.Mgr.Prompt(context.Background(), specFix.Sess.ID, "hello"); err != nil {
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
	if _, err := buildFix.Mgr.Prompt(context.Background(), buildFix.Sess.ID, "hello"); err != nil {
		testutil.FailErr(t, "buildFix.Mgr.Prompt failed", err)
	}
	buildSession, err := buildFix.Store.Get(t.Context(), buildFix.Sess.ID)
	testutil.FailErr(t, "read attached workflow posture", err)
	if buildSession.Posture != api.SessionPostureBuild {
		t.Fatalf("default workflow posture = %s, want build", buildSession.Posture)
	}
	run, err := buildFix.Workflow.GetActive(t.Context(), buildFix.Sess.ID)
	testutil.FailErr(t, "read attached workflow", err)
	if run == nil || !buildFix.Workflow.IsAmbientRun(run) {
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
	fix.Mgr.Loading.SetLedger(turnload.NewLedger())
	fix.Mgr.SetPromptEngine(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{}))
	if _, err := fix.Mgr.Prompt(context.Background(), fix.Sess.ID, "run a bounded local-service workflow"); err != nil {
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
