package prompts_test

import (
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolsurface"
)

// The debug snapshot carries every command guidance fact and follows whether
// command is offered.
func TestCommandGuidanceSnapshotFollowsOfferedCommand(t *testing.T) {
	profiles, err := sandbox.LoadToolProfiles()
	testutil.FailErr(t, "load tool profiles", err)
	snapshot := func(offered []string) ([]string, map[string]any) {
		reader, err := prompts.NewReader(prompts.HostFacts{Profiles: profiles, RootCount: 2, WebSearch: true}, prompts.TurnProjection{
			ProfileID: prompts.CoordinatorProfileID,
			SurfaceID: "implement_investigate",
			Plan:      toolsurface.Compile(offered, []string{"edit", "jq"}),
			Offered:   offered,
		})
		testutil.FailErr(t, "new reader", err)
		return prompts.BuildCommandGuidanceSnapshot(reader, prompts.CommandGuidanceSnapshotInput{
			ProjectDir: "/workspace/app",
			Posture:    gate.PostureStrict,
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
	if facts["profile_has_command"] != true {
		t.Fatalf("profile_has_command = %#v with command offered", facts["profile_has_command"])
	}
	_, without := snapshot([]string{"wait", "request_tools"})
	if without["profile_has_command"] != false {
		t.Fatalf("profile_has_command = %#v without command", without["profile_has_command"])
	}
}
