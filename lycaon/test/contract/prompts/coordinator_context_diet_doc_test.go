package contract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestCompactionConfigDocumentsBudgetKnobs(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "lycaon", "config", "packs", "painted-wolf", "platform", "host", "compaction.yaml"))
	contractcheck.FailErr(t, "read compaction.yaml", err)
	text := string(data)
	for _, required := range []string{
		"window_source",
		"live_budget_pct",
		"budget_trigger_pct",
		"target_tokens_pct",
		"chunk_strategy_default",
		"max_citation_grounding_retries",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("compaction.yaml missing budget knob %q", required)
		}
	}
}

func TestCoordinatorSurfacesYAMLExists(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	path := filepath.Join(root, "lycaon", "config", "packs", "painted-wolf", "platform", "host", "coordinator-surfaces.yaml")
	data, err := os.ReadFile(path)
	contractcheck.FailErr(t, "read coordinator-surfaces.yaml", err)
	text := string(data)
	for _, id := range []string{
		"implement_routing:",
		"implement_synthesis:",
		"workflow_compose:",
		"plan_stub:",
		"plan_research:",
		"plan_approve:",
		"await_user:",
		"plan_execute:",
	} {
		if !strings.Contains(text, id) {
			t.Fatalf("coordinator-surfaces.yaml missing surface id %q", id)
		}
	}
}
