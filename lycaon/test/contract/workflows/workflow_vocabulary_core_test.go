package contract

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/vocabulary"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestWorkflowVocabularyCoreLayer(t *testing.T) {
	t.Parallel()
	for _, row := range vocabulary.ExportCatalog() {
		if row.Status != "shipped" {
			continue
		}
		if row.Domain != "—" && row.Domain != "" {
			continue
		}
		if row.Layer != "core" {
			t.Fatalf("shipped core id %q has layer %q", row.ID, row.Layer)
		}
	}
}

func TestBundledWorkflowsNoForbiddenStageComplete(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	dir := filepath.Join(root, "lycaon", "config", "packs", "painted-wolf", "platform", "workflows")
	entries, err := os.ReadDir(dir)
	contractcheck.FailErr(t, "read directory entries", err)
	forbiddenStageComplete := regexp.MustCompile(`stage_[a-z0-9_]+_complete`)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		contractcheck.FailErr(t, "read file", err)
		text := string(data)
		for _, line := range strings.Split(text, "\n") {
			trim := strings.TrimSpace(line)
			if forbiddenStageComplete.MatchString(trim) {
				t.Fatalf("%s contains forbidden stage_*_complete reference: %s", e.Name(), trim)
			}
		}
		if strings.Contains(text, "user_input_") {
			t.Fatalf("%s contains forbidden user_input_* id", e.Name())
		}
	}
}

func TestCoreParameterizedFamiliesInCatalog(t *testing.T) {
	t.Parallel()
	want := map[string]struct{}{
		"phase_is:*":               {},
		"evidence_passed:*":        {},
		"user_feedback_received:*": {},
		"user_decision_received:*": {},
		"agent_is:*":               {},
		"topology_stage_complete":  {},
	}
	seen := map[string]struct{}{}
	for _, row := range vocabulary.ExportCatalog() {
		if row.Status == "shipped" {
			seen[row.ID] = struct{}{}
		}
	}
	for id := range want {
		if _, ok := seen[id]; !ok {
			t.Fatalf("missing shipped catalog entry %q", id)
		}
	}
}
