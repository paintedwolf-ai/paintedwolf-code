//go:build integration

package composition_test

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	workflowcomposition "github.com/lycaon/lycaon/internal/workflow/composition"
)

func TestComposeSetsRequiresIsolation(t *testing.T) {
	c := testComposer(t)

	packManifest := `id: pack-session
version: 1.0.0
extends: plan@1.0.0
topology: pack-probe
initial_posture: spec
agents:
  - { id: coordinator, tools: all }
  - { id: implementer, tools: all }
phases:
  - id: research
    activity_label: Test phase
    blueprint_write: true
    coordinator_surface: plan_stub
    surface_template: agents/coordinator-surface-plan.md
    complete_when: plan_stub_valid
    next: build
  - id: build
    activity_label: Test phase
    complete_when: delegation_closeout_complete
`
	result, err := c.Compose(context.Background(), workflowcomposition.ComposeRequest{
		SessionID:    "sess-compose-isolation",
		ProjectDir:   t.TempDir(),
		ManifestYAML: []byte(packManifest),
		DryRun:       true,
	})
	testutil.FailErr(t, "c.Compose failed", err)
	if !result.EffectiveSummary.RequiresIsolation {
		t.Fatal("expected requires_isolation for pack-probe topology")
	}

	pipelineManifest := `id: pipeline-session
version: 1.0.0
extends: plan@1.0.0
topology: default-pipeline
initial_posture: spec
agents:
  - { id: coordinator, tools: all }
phases:
  - id: research
    activity_label: Test phase
    blueprint_write: true
    coordinator_surface: plan_stub
    surface_template: agents/coordinator-surface-plan.md
    complete_when: plan_stub_valid
    next: build
  - id: build
    activity_label: Test phase
    complete_when: delegation_closeout_complete
`
	result2, err := c.Compose(context.Background(), workflowcomposition.ComposeRequest{
		SessionID:    "sess-pipeline",
		ProjectDir:   t.TempDir(),
		ManifestYAML: []byte(pipelineManifest),
		DryRun:       true,
	})
	testutil.FailErr(t, "c.Compose failed", err)
	if result2.EffectiveSummary.RequiresIsolation {
		t.Fatal("pipeline topology should not set requires_isolation")
	}
}
