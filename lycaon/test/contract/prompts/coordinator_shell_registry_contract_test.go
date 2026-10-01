package contract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/surface"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"gopkg.in/yaml.v3"
)

var coordinatorFamilyShellTopics = map[string][]string{
	"coordinator-mode-shell-investigate.md": {
		"coordinator-mode-investigate-progress.md",
	},
	"coordinator-mode-shell-orchestrate.md": {
		"coordinator-mode-orchestrate-invariants-edits.md",
		"coordinator-mode-orchestrate-pacing.md",
		"coordinator-mode-orchestrate-cycle.md",
	},
}

// Pacing guidance paths are relative to the platform pack's shared/ directory.
var coordinatorFamilyPacingBatchDispatch = map[string]string{
	"units/investigate-pacing.md":                     "partials/coordinator-batch-dispatch.md",
	"partials/coordinator-mode-orchestrate-pacing.md": "partials/coordinator-batch-dispatch.md",
}

func TestCoordinatorFamilyShellRegistry(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	partialsDir := filepath.Join(root, "lycaon", "config", "packs", "painted-wolf", "platform", "shared", "partials")
	for shell, topics := range coordinatorFamilyShellTopics {
		shellPath := filepath.Join(partialsDir, shell)
		raw, err := os.ReadFile(shellPath)
		if err != nil {
			t.Fatalf("read shell %s: %v", shell, err)
		}
		body := string(raw)
		if !strings.Contains(body, "{{ units.execution }}") {
			t.Fatalf("shell %s does not place the execution unit slot", shell)
		}
		for _, topic := range topics {
			if !strings.Contains(body, topic) {
				t.Fatalf("shell %s missing include of %s", shell, topic)
			}
			if _, err := os.Stat(filepath.Join(partialsDir, topic)); err != nil {
				t.Fatalf("topic file missing %s: %v", topic, err)
			}
		}
	}
}

func TestCoordinatorFamilyPacingIncludesBatchDispatch(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	sharedDir := filepath.Join(root, "lycaon", "config", "packs", "painted-wolf", "platform", "shared")
	if _, err := os.Stat(filepath.Join(sharedDir, "partials", "coordinator-batch-dispatch.md")); err != nil {
		t.Fatalf("shared batch-dispatch partial missing: %v", err)
	}
	for pacing, include := range coordinatorFamilyPacingBatchDispatch {
		raw, err := os.ReadFile(filepath.Join(sharedDir, filepath.FromSlash(pacing)))
		if err != nil {
			t.Fatalf("read pacing %s: %v", pacing, err)
		}
		if !strings.Contains(string(raw), include) {
			t.Fatalf("pacing %s missing include of %s", pacing, include)
		}
	}
}

func TestExecutionModeFamilyCoversCoordinatorSurfacesYAML(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	path := filepath.Join(root, "lycaon", "config", "packs", "painted-wolf", "platform", "host", "coordinator-surfaces.yaml")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read surfaces yaml: %v", err)
	}
	var surfaces map[string]any
	if err := yaml.Unmarshal(raw, &surfaces); err != nil {
		t.Fatalf("unmarshal surfaces: %v", err)
	}
	for surfaceID := range surfaces {
		switch surfaceID {
		case "workflow_compose", "plan_stub", "plan_research", "plan_approve", "await_user", "plan_execute":
			continue
		}
		if strings.HasPrefix(surfaceID, "implement_") {
			if got := surface.ExecutionModeFamily(surfaceID); got == "" {
				t.Fatalf("ExecutionModeFamily(%q) empty — orchestrate/implement surfaces must map", surfaceID)
			}
		}
	}
}
