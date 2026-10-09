package definition_test

import (
	"github.com/lycaon/lycaon/internal/testutil"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"os"
	"path/filepath"
	"testing"
)

func TestTransitionOnlyPhaseSurvivesFinalize(t *testing.T) {
	path := filepath.Join("..", "..", "..", "config", "fixtures", "workflows", "choice-transitions.yaml")
	raw, err := os.ReadFile(path)
	testutil.FailErr(t, "read fixture", err)
	m, err := workflowdef.ParseManifestYAML(raw)
	testutil.FailErr(t, "ParseManifestYAML", err)
	m = workflowdef.FinalizeManifest(m)

	if _, ok := m.PhaseByID("side"); !ok {
		t.Fatal("side phase missing after FinalizeManifest — T31 keep-set must include transitions[].to")
	}
	if _, ok := m.PhaseByID("review"); !ok {
		t.Fatal("review phase missing after FinalizeManifest")
	}
	if _, ok := m.PhaseByID("research_more"); !ok {
		t.Fatal("research_more phase missing after FinalizeManifest")
	}
	// Linear spine stays next-only (decide → done); off-spine not required in Phases.
	for _, id := range m.Phases {
		if id == "side" {
			t.Fatal("side must not be on next-only Phases spine")
		}
	}
	keep := workflowdef.ReachablePhaseIDs(m.PhaseDefs)
	if _, ok := keep["side"]; !ok {
		t.Fatal("reachablePhaseIDs missing side")
	}
}

func TestChoiceTransitionsParseMultiArm(t *testing.T) {
	path := filepath.Join("..", "..", "..", "config", "fixtures", "workflows", "choice-transitions.yaml")
	raw, err := os.ReadFile(path)
	testutil.FailErr(t, "read fixture", err)
	m, err := workflowdef.ParseManifestYAML(raw)
	testutil.FailErr(t, "ParseManifestYAML", err)
	m = workflowdef.FinalizeManifest(m)
	decide, ok := m.PhaseByID("decide")
	if !ok {
		t.Fatal("decide missing")
	}
	if len(decide.Transitions) < 2 {
		t.Fatalf("want ≥2 transitions, got %d", len(decide.Transitions))
	}
	humanArms := 0
	for _, tr := range decide.Transitions {
		for _, a := range tr.Actors {
			if a == workflowdef.TransitionActorHuman {
				humanArms++
				break
			}
		}
	}
	if humanArms < 2 {
		t.Fatalf("want ≥2 human arms, got %d", humanArms)
	}
}

func TestChoiceTransitionLoaderRejects(t *testing.T) {
	cases := []struct {
		name string
		yaml string
	}{
		{
			name: "unknown to",
			yaml: `
id: bad-to
version: 1.0.0
phases:
  - id: a
    activity_label: Test phase
    complete_when: gates_satisfied
    gates: [human_approval]
    next: done
    transitions:
      - id: go
        to: missing
        actors: [human]
        label: Go
  - id: done
    activity_label: Test phase
    terminal: true
    complete_when: orchestration_complete
`,
		},
		{
			name: "self-edge",
			yaml: `
id: bad-self
version: 1.0.0
phases:
  - id: a
    activity_label: Test phase
    complete_when: gates_satisfied
    gates: [human_approval]
    next: done
    transitions:
      - id: loop
        to: a
        actors: [human]
        label: Loop
  - id: done
    activity_label: Test phase
    terminal: true
    complete_when: orchestration_complete
`,
		},
		{
			name: "bad actor",
			yaml: `
id: bad-actor
version: 1.0.0
phases:
  - id: a
    activity_label: Test phase
    complete_when: gates_satisfied
    gates: [human_approval]
    next: b
    transitions:
      - id: go
        to: b
        actors: [auto]
        label: Go
  - id: b
    activity_label: Test phase
    terminal: true
    complete_when: orchestration_complete
`,
		},
		{
			name: "duplicate id",
			yaml: `
id: bad-dup
version: 1.0.0
phases:
  - id: a
    activity_label: Test phase
    complete_when: gates_satisfied
    gates: [human_approval]
    next: b
    transitions:
      - id: go
        to: b
        actors: [human]
        label: One
      - id: go
        to: b
        actors: [coordinator]
        label: Two
  - id: b
    activity_label: Test phase
    terminal: true
    complete_when: orchestration_complete
`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := workflowdef.ParseManifestYAML([]byte(tc.yaml))
			if err == nil {
				t.Fatal("expected loader error")
			}
		})
	}
}
