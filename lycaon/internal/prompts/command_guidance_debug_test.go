package prompts_test

import (
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/prompts"
)

// The debug snapshot carries every command guidance fact and follows whether
// command is offered.
func TestCommandGuidanceSnapshotFollowsOfferedCommand(t *testing.T) {
	snapshot := func(offered []string) ([]string, map[string]any) {
		return prompts.BuildCommandGuidanceSnapshot(prompts.CommandGuidanceSnapshotInput{
			ProfileID:     prompts.CoordinatorProfileID,
			SurfaceID:     "implement_investigate",
			IsCoordinator: true,
			OfferedTools:  offered,
			DeferredTools: []string{"edit", "jq"},
			RootCount:     2,
			ProjectDir:    "/workspace/app",
			Posture:       gate.PostureStrict,
		})
	}
	templates, facts := snapshot([]string{"command", "command_output", "wait", "request_tools"})
	if !slices.Contains(templates, "tools/schemas/command.yaml") {
		t.Fatalf("templates = %v, want the command schema", templates)
	}
	for _, key := range prompts.CommandGuidanceFactKeys() {
		if key == "workspace_roots" {
			continue
		}
		if _, ok := facts[key]; !ok {
			t.Errorf("facts missing %q", key)
		}
	}
	if facts["command_output_files"] != true {
		t.Fatalf("command_output_files = %#v with command offered", facts["command_output_files"])
	}
	_, without := snapshot([]string{"wait", "request_tools"})
	if without["command_output_files"] != false {
		t.Fatalf("command_output_files = %#v without command", without["command_output_files"])
	}
}
