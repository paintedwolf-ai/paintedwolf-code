package profiles

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/pkg/api"
)

type commandSessionFixture struct{ posture api.SessionPosture }

func (f commandSessionFixture) Get(context.Context, string) (*api.Session, error) {
	return &api.Session{ID: "session", Posture: f.posture}, nil
}

func TestCommandProfilesUseActualPostureAndPreserveRetainedPolicy(t *testing.T) {
	registry, err := LoadPostureRegistry()
	testutil.FailErr(t, "load actual postures", err)
	all, _ := toolcontract.NewCommandEffects(toolcontract.CommandReadOnly, toolcontract.CommandMutation, toolcontract.CommandExternalExec, toolcontract.CommandNetworkEgress)
	read, _ := toolcontract.NewCommandEffects(toolcontract.CommandReadOnly)
	input := []sandbox.ToolProfile{{ID: "coordinator", CommandEffects: all, DenyTools: []string{"fixture-denial"}}}
	for _, posture := range []api.SessionPosture{api.SessionPostureSpec, api.SessionPostureVet, api.SessionPostureBuild, api.SessionPostureOrchestrate} {
		service := New(commandSessionFixture{posture: posture}, nil, nil)
		service.SetPostureRegistry(registry)
		output := service.ConstrainCommandProfiles(t.Context(), "session", input)
		want := all
		if posture == api.SessionPostureSpec || posture == api.SessionPostureVet {
			want = read
		}
		if len(output) != 1 || output[0].CommandEffects != want || !output[0].ToolDenied("fixture-denial") {
			t.Fatalf("posture %s changed effect or retained policy: %+v", posture, output)
		}
		if input[0].CommandEffects != all {
			t.Fatal("posture mutated catalog profile")
		}
	}
	if output := (*Service)(nil).ConstrainCommandProfiles(t.Context(), "session", input); output[0].CommandEffects != 0 {
		t.Fatal("missing posture service widened native surface")
	}
}

func TestShippedReadOnlyProfilesDeclareOnlyNativeReads(t *testing.T) {
	profiles, err := sandbox.LoadToolProfiles()
	testutil.FailErr(t, "load actual profile catalog", err)
	read, _ := toolcontract.NewCommandEffects(toolcontract.CommandReadOnly)
	for _, id := range []string{"worker_readonly", "explore_readonly", "plan_review_readonly"} {
		found := false
		for _, profile := range profiles {
			if profile.ID == id {
				found = true
				if profile.CommandEffects != read {
					t.Fatalf("%s permits %v", id, profile.CommandEffects.Classes())
				}
				if !profile.ToolAllowed("command") || !profile.StickyTools["command"] {
					t.Fatalf("%s does not offer sticky read-only command", id)
				}
			}
		}
		if !found {
			t.Fatalf("missing shipped profile %s", id)
		}
	}
}

func TestWorkerChildSessionPostureAndConstrainedCommandProfiles(t *testing.T) {
	registry, err := LoadPostureRegistry()
	testutil.FailErr(t, "load actual postures", err)

	read, _ := toolcontract.NewCommandEffects(toolcontract.CommandReadOnly)
	readMut, _ := toolcontract.NewCommandEffects(toolcontract.CommandReadOnly, toolcontract.CommandMutation)
	readEgress, _ := toolcontract.NewCommandEffects(toolcontract.CommandReadOnly, toolcontract.CommandNetworkEgress)
	all, _ := toolcontract.NewCommandEffects(toolcontract.CommandReadOnly, toolcontract.CommandMutation, toolcontract.CommandExternalExec, toolcontract.CommandNetworkEgress)

	profileEffects := func(profiles []sandbox.ToolProfile, id string) toolcontract.CommandEffects {
		for _, p := range profiles {
			if p.ID == id {
				return p.CommandEffects
			}
		}
		t.Fatalf("missing profile %s", id)
		return 0
	}

	for _, parentPosture := range []api.SessionPosture{api.SessionPostureSpec, api.SessionPostureVet, api.SessionPostureBuild, api.SessionPostureOrchestrate} {
		if got := store.ChildSessionPosture(parentPosture, "implementer"); got != api.SessionPostureBuild {
			t.Fatalf("implementer child posture under parent %s = %s, want build", parentPosture, got)
		}
		for _, agentType := range []string{"plan-author", "web-researcher", "path-explorer", "plan-reviewer", "general"} {
			if got := store.ChildSessionPosture(parentPosture, agentType); got != parentPosture {
				t.Fatalf("%s child posture under parent %s = %s, want %s", agentType, parentPosture, got, parentPosture)
			}
		}

		service := New(commandSessionFixture{posture: parentPosture}, nil, nil)
		service.SetPostureRegistry(registry)
		narrowed := service.ForSession(t.Context(), &api.Session{ID: "session", Posture: parentPosture})
		if len(service.ForSession(t.Context(), nil)) == 0 {
			t.Fatal("expected fallback profiles for nil session")
		}

		readOnlyPosture := parentPosture == api.SessionPostureSpec || parentPosture == api.SessionPostureVet
		if got := profileEffects(narrowed, "plan_write_only"); (readOnlyPosture && got != read) || (!readOnlyPosture && got != readMut) {
			t.Fatalf("plan_write_only under %s = %v", parentPosture, got.Classes())
		}
		if got := profileEffects(narrowed, "editor_file"); (readOnlyPosture && got != read) || (!readOnlyPosture && got != readMut) {
			t.Fatalf("editor_file under %s = %v", parentPosture, got.Classes())
		}
		if got := profileEffects(narrowed, "web_research"); (readOnlyPosture && got != read) || (!readOnlyPosture && got != readEgress) {
			t.Fatalf("web_research under %s = %v", parentPosture, got.Classes())
		}
		if got := profileEffects(narrowed, "implement"); (readOnlyPosture && got != read) || (!readOnlyPosture && got != all) {
			t.Fatalf("implement under %s = %v", parentPosture, got.Classes())
		}
		for _, roID := range []string{"worker_readonly", "explore_readonly", "plan_review_readonly"} {
			if got := profileEffects(narrowed, roID); got != read {
				t.Fatalf("%s under %s = %v, want [read_only]", roID, parentPosture, got.Classes())
			}
		}
	}
}
