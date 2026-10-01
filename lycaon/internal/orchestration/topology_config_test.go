package orchestration

import (
	"context"
	"fmt"
	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/extpacks"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func bundledTopologyPath(t *testing.T, name string) extpacks.Source {
	t.Helper()
	return extpacks.Bundled(config.PlatformFlows.Join("_topologies", name))
}

func TestLoadDefaultPipelineTopology(t *testing.T) {
	spec, err := LoadTopologyFromFile(bundledTopologyPath(t, "default-pipeline.yaml"))
	testutil.FailErr(t, "LoadTopologyFromFile failed", err)
	if spec.Pipeline == nil || len(spec.Pipeline.Stages) != 6 {
		t.Fatalf("stages = %d want 6", len(spec.Pipeline.Stages))
	}
	wantProfiles := map[string]string{
		"research":  "repo-researcher",
		"plan":      "coordinator",
		"implement": "implementer",
		"review":    "code-reviewer",
		"test":      "implementer",
		"closeout":  "coordinator",
	}
	for _, stage := range spec.Pipeline.Stages {
		want, ok := wantProfiles[stage.Name]
		if !ok {
			t.Fatalf("unexpected stage %q", stage.Name)
		}
		if stage.AgentProfile != want {
			t.Fatalf("stage %q profile = %q want %q", stage.Name, stage.AgentProfile, want)
		}
	}
}

func TestLoadBugbashTopologyEndsWithReadOnlyTriage(t *testing.T) {
	spec, err := LoadTopologyFromFile(bundledTopologyPath(t, "bugbash.yaml"))
	testutil.FailErr(t, "LoadTopologyFromFile bugbash", err)
	if spec.Pipeline == nil || len(spec.Pipeline.Stages) != 4 {
		t.Fatalf("stages = %d want 4", len(spec.Pipeline.Stages))
	}
	for _, stage := range spec.Pipeline.Stages {
		if stage.Name == "triage" && stage.AgentProfile != "code-reviewer" {
			t.Fatalf("stage %q profile = %q want code-reviewer", stage.Name, stage.AgentProfile)
		}
	}
}

func TestLoadTopologyValidatesProfiles(t *testing.T) {
	reg := loadAgentRegistryFromConfig(t)
	orch := NewOrchestratorImpl(OrchestratorDeps{Agents: reg})
	spec, err := orch.LoadTopology(context.Background(), bundledTopologyPath(t, "default-pipeline.yaml"))
	testutil.FailErr(t, "orch.LoadTopology failed", err)
	if spec.ID != "default-pipeline" {
		t.Fatalf("id = %q", spec.ID)
	}
}

func TestPipelineStageGraphOrder(t *testing.T) {
	spec, err := LoadTopologyFromFile(bundledTopologyPath(t, "default-pipeline.yaml"))
	testutil.FailErr(t, "LoadTopologyFromFile failed", err)
	order, err := pipelineDispatchOrder(spec.Pipeline.Stages)
	testutil.FailErr(t, "pipelineDispatchOrder failed", err)
	if order[0] != "research" {
		t.Fatalf("first stage = %q want research", order[0])
	}
	if order[len(order)-1] != "closeout" {
		t.Fatalf("last stage = %q want closeout", order[len(order)-1])
	}
	if !pipelineMustFollow(spec.Pipeline.Stages, "implement", "review") {
		t.Fatal("review should follow implement")
	}
	if !pipelineMustFollow(spec.Pipeline.Stages, "implement", "test") {
		t.Fatal("test should follow implement")
	}
	if !pipelineMustFollow(spec.Pipeline.Stages, "research", "plan") {
		t.Fatal("plan should follow research")
	}
	researchIdx := indexOf(order, "research")
	planIdx := indexOf(order, "plan")
	implementIdx := indexOf(order, "implement")
	if researchIdx >= planIdx || planIdx >= implementIdx {
		t.Fatalf("bad order: %v", order)
	}
}

func indexOf(items []string, target string) int {
	for i, v := range items {
		if v == target {
			return i
		}
	}
	return -1
}

func TestPipelineStageNamesUseSymbolicIDs(t *testing.T) {
	for _, tc := range []struct {
		name  string
		valid bool
	}{
		{"stage-2_review", true}, {"0", true}, {"_", true},
		{"", false}, {"Review", false}, {"stage one", false},
		{" stage", false}, {"stage/one", false}, {"stage.one", false}, {"étape", false},
	} {
		t.Run(fmt.Sprintf("%q", tc.name), func(t *testing.T) {
			directErr := validatePipelineStages([]PipelineStage{{Name: tc.name, Label: "Review"}})
			yaml := fmt.Sprintf("id: test\npattern: pipeline\npipeline:\n  stages:\n    - name: %q\n      label: Review\n      profile: path-explorer\n", tc.name)
			_, parseErr := ParseTopology([]byte(yaml))
			if (directErr == nil) != tc.valid || (parseErr == nil) != tc.valid {
				t.Fatalf("name %q valid=%v: direct=%v parsed=%v", tc.name, tc.valid, directErr, parseErr)
			}
		})
	}
}
