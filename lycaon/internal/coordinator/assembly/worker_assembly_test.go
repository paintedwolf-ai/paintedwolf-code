package assembly

import (
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"testing"
)

func TestBuildWorkerPromptContextDefaultTopology(t *testing.T) {
	got := inject.BuildWorkerPromptContext(inject.WorkerLegContext{})
	if got.TopologyPattern != "pipeline" {
		t.Fatalf("topology = %q want pipeline", got.TopologyPattern)
	}
}

func TestExtractProcessBullets(t *testing.T) {
	rendered := "## Focus\nignored\n\n## Process\n\n1. Read the plan\n- Write tests\n\n## Tools\n\n- read"
	got := ExtractProcessBullets(rendered)
	if len(got) != 2 || got[0] != "Read the plan" || got[1] != "Write tests" {
		t.Fatalf("bullets = %v", got)
	}
}

func TestFilterChecklistDedup(t *testing.T) {
	checklist := []string{"Read the plan", "Run tests"}
	process := []string{"Read the plan"}
	got := FilterChecklistDedup(checklist, process)
	if len(got) != 1 || got[0] != "Run tests" {
		t.Fatalf("checklist = %v", got)
	}
}
