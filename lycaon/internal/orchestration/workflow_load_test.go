package orchestration

import (
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/extpackstest"
)

// The merged catalog resolves workflow and topology units together.
func TestLoadBugbashManifest(t *testing.T) {
	stock := extpackstest.StockCatalog(t)
	ref, err := LoadWorkflowManifest(stock, "bugbash", "1.0.0")
	testutil.FailErr(t, "LoadWorkflowManifest failed", err)
	if ref.ID != "bugbash" {
		t.Fatalf("id = %q", ref.ID)
	}
	if ref.Version != "1.0.0" {
		t.Fatalf("version = %q", ref.Version)
	}
	if ref.TopologyID != "bugbash" {
		t.Fatalf("topology = %q want bugbash", ref.TopologyID)
	}
	if len(ref.BoundPhases) == 0 {
		t.Fatal("bugbash binds a parallel group; BoundPhases must not be empty")
	}
	for stage, phase := range map[string]string{
		"hunt_correctness": "hunt",
		"hunt_edges":       "hunt",
		"hunt_races":       "hunt",
		"triage":           "triage",
	} {
		if !ref.StagePhases[stage][phase] {
			t.Fatalf("stage %q phases = %v want %q", stage, ref.StagePhases[stage], phase)
		}
	}
	spec, err := TopologySpecForID(stock, ref.TopologyID)
	testutil.FailErr(t, "TopologySpecForID failed", err)
	if spec.Pattern != TopologyPipeline {
		t.Fatalf("pattern = %q", spec.Pattern)
	}
	if spec.Pipeline == nil || len(spec.Pipeline.Stages) != 4 {
		t.Fatalf("stages = %d want 4", len(spec.Pipeline.Stages))
	}
	if path := TopologyPathForID(stock, ref.TopologyID); path.Empty() {
		t.Fatal("topology unit must publish a path")
	}
}
