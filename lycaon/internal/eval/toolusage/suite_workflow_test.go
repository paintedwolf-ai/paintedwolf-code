package toolusage

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workflow"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
)

func TestWorkflowFixturesUseValidProjectManifests(t *testing.T) {
	suite, err := LoadSuite("../../../test/fixtures/eval/coordinator-benchmark.json")
	testutil.FailErr(t, "load benchmark", err)
	count := 0
	for _, spec := range suite.Cases {
		if spec.WorkflowID == "" {
			continue
		}
		count++
		t.Run(spec.ID, func(t *testing.T) {
			path := filepath.Join(suite.root, spec.Project, settingsoverlay.DirName(), "workflows", spec.WorkflowID, "workflow.yaml")
			body, err := os.ReadFile(path)
			testutil.FailErr(t, "read workflow", err)
			manifest, err := workflowdef.ParseManifestYAML(body)
			testutil.FailErr(t, "parse SDK workflow", err)
			if manifest.ID != spec.WorkflowID || manifest.Version != spec.WorkflowVersion {
				t.Fatalf("workflow identity: %q", manifest.ID)
			}
			if issues := workflow.ValidatePrimitiveManifest(manifest); len(issues) > 0 {
				t.Fatalf("workflow primitives: %+v", issues)
			}
			if issues := workflow.ValidatePhaseReachability(manifest); len(issues) > 0 {
				t.Fatalf("workflow reachability: %+v", issues)
			}
			for _, phase := range manifest.PhaseDefs {
				if phase.Terminal {
					continue
				}
				_, err = os.Stat(filepath.Join(suite.root, spec.Project, settingsoverlay.DirName(), "prompt_files", phase.SurfaceTemplate))
				testutil.FailErr(t, "read phase template", err)
			}
		})
	}
	if count == 0 {
		t.Fatal("no custom workflows exercised")
	}
}
