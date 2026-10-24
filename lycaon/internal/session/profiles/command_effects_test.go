package profiles

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/sandbox"
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
				if !profile.ToolDenied("command") {
					t.Fatal("native metadata prematurely activated old shell command")
				}
			}
		}
		if !found {
			t.Fatalf("missing shipped profile %s", id)
		}
	}
}
