//go:build integration

package session_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	coordinatorsurface "github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/coordinator/turnload"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
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
	for _, test := range []struct {
		name  string
		args  map[string]any
		valid bool
	}{
		{"program with capabilities", map[string]any{"command": `MODE=fixture; printf '%s\n' "$MODE"`, "capability_request": map[string]any{"socket_paths": []string{filepath.Join(t.TempDir(), "service.sock")}, "direct_ip": true}}, true},
		{"program string required", map[string]any{"command": 12}, false},
		{"socket path array required", map[string]any{"command": "true", "capability_request": map[string]any{"socket_paths": "service.sock"}}, false},
		{"environment belongs in program", map[string]any{"command": "true", "env": map[string]any{"MODE": "fixture"}}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := tools.ValidateToolArgs(commandSchema, test.args)
			if (err == nil) != test.valid {
				t.Fatalf("actual offered arguments valid=%v want=%v error=%v", err == nil, test.valid, err)
			}
		})
	}

	if _, ok := firstSystemMessage(req.Messages); !ok {
		t.Fatal("Investigate request must include a system prompt")
	}
}
