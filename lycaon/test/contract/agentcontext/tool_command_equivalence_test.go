package contract

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/surfacecatalog"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/observability"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestRoutineFloorsOfferCommandInPlaceOfRetiredSchemas(t *testing.T) {
	t.Parallel()
	retiredOperations, err := toolcontract.CommandOperations()
	contractcheck.FailErr(t, "load command operations", err)
	catalog, err := surfacecatalog.Load()
	contractcheck.FailErr(t, "load coordinator surfaces", err)
	for _, id := range catalog.SurfaceIDs() {
		surface, err := catalog.Surface(id)
		contractcheck.FailErr(t, "read surface "+id, err)
		floor := surface.FloorTools()
		for _, retired := range retiredOperations {
			if slices.Contains(floor, retired) || slices.Contains(surface.LoadableTools(), retired) {
				t.Errorf("%s still offers command operation %s as a tool", id, retired)
			}
		}
		if slices.Contains(floor, "command") && slices.Contains(surface.LoadableTools(), "command") {
			t.Errorf("%s lists command on both its floor and its loadable set", id)
		}
	}
	for _, id := range []string{"implement_investigate", "observe_investigate", "plan_review", "await_host"} {
		surface, err := catalog.Surface(id)
		if err != nil || !slices.Contains(surface.FloorTools(), "command") {
			t.Errorf("%s does not offer command on its floor", id)
		}
	}
}

func TestToolProfilesOmitCommandOperationsAndOfferReadOnlyCommand(t *testing.T) {
	t.Parallel()
	retiredOperations, err := toolcontract.CommandOperations()
	contractcheck.FailErr(t, "load command operations", err)
	for _, id := range []string{"coordinator", "implement", "worker_readonly", "explore_readonly", "plan_review_readonly"} {
		profile := loadToolProfileByID(t, id)
		for _, retired := range retiredOperations {
			if profile.ToolAllowed(retired) || profile.StickyTools[retired] {
				t.Errorf("%s offers command operation %s as a tool", id, retired)
			}
		}
		if !profile.ToolAllowed("command") || !profile.StickyTools["command"] {
			t.Errorf("%s does not offer command up front", id)
		}
	}
	for _, id := range []string{"worker_readonly", "explore_readonly", "plan_review_readonly"} {
		if classes := loadToolProfileByID(t, id).CommandEffects.Classes(); !slices.Equal(classes, []toolcontract.CommandEffectClass{toolcontract.CommandReadOnly}) {
			t.Errorf("%s command is not read-only", id)
		}
	}
}

func TestCommandGuidanceDebugCaptureInLLMRequestsJSONL(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "llm-requests.jsonl")
	t.Setenv("LYCAON_LLM_DEBUG", "1")
	t.Setenv("LYCAON_LLM_DEBUG_FILE", logPath)
	observability.CloseLLMDebug()
	t.Cleanup(observability.CloseLLMDebug)

	templates, facts := prompts.BuildCommandGuidanceSnapshot(prompts.CommandGuidanceSnapshotInput{
		ProfileID:             prompts.CoordinatorProfileID,
		SurfaceID:             "implement_investigate",
		IsCoordinator:         true,
		OfferedTools:          []string{"command", "command_output", "command_stop", "wait", "process_list", "process_signal", "request_tools"},
		DeferredTools:         []string{"read", "edit", "grep", "find", "list_dir", "jq"},
		RootCount:             2,
		ProjectDir:            "/workspace/app",
		SessionScratchDir:     "/scratch/sess-debug",
		Posture:               gate.PostureStrict,
		CommandExternalHidden: false,
	})
	observability.LogLLMRequest(
		"mock", "mock-model", "stream",
		[]api.Message{{Role: api.MessageRoleUser, Content: "inspect repo"}},
		nil,
		observability.LLMRequestTiming{DurationMs: 10, TTFTMs: 4},
		observability.LLMUsageCapture{PromptTokens: 100, CompletionTokens: 10},
		observability.LLMRequestDebug{
			SessionID: "sess-debug",
			ProfileID: prompts.CoordinatorProfileID,
			Surface:   "implement_investigate",
			Iteration: 1,
			CommandGuidance: &observability.LLMCommandGuidanceCapture{
				Templates: templates,
				Facts:     facts,
			},
		},
		nil, "",
	)

	raw, err := os.ReadFile(logPath)
	contractcheck.FailErr(t, "read llm-requests.jsonl", err)
	var row struct {
		CommandGuidance *observability.LLMCommandGuidanceCapture `json:"command_guidance"`
	}
	contractcheck.FailErr(t, "unmarshal capture row", json.Unmarshal([]byte(strings.TrimSpace(string(raw))), &row))
	if row.CommandGuidance == nil {
		t.Fatal("capture row missing command_guidance")
	}
	for _, wantTmpl := range []string{
		"tools/schemas/command.yaml",
		"partials/command-surface.md",
		"units/coordinator-host-runner.md",
		"units/secure-by-default-run.md",
		"units/runner-operating-procedure.md",
		"partials/capability-request-grammar.md",
	} {
		if !slices.Contains(row.CommandGuidance.Templates, wantTmpl) {
			t.Errorf("command_guidance.templates = %v, missing %q", row.CommandGuidance.Templates, wantTmpl)
		}
	}
	if row.CommandGuidance.Facts["command_output_files"] != true {
		t.Fatalf("debug output capability differs from offered command: %#v", row.CommandGuidance.Facts["command_output_files"])
	}
	for _, key := range prompts.CommandGuidanceFactKeys() {
		if key == "workspace_roots" {
			continue
		}
		if _, ok := row.CommandGuidance.Facts[key]; !ok {
			t.Errorf("command_guidance.facts missing key %q", key)
		}
	}
}

