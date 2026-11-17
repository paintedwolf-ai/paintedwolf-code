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
