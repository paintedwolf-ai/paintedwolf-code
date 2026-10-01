package contract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// TestSummarizeScaleInvariantCapsWired pins the pack caps
// appear in shipped YAML and are read outside caps.go (drift-guard companion).
func TestSummarizeScaleInvariantCapsWired(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	yamlPath := filepath.Join(root, "lycaon", "config", "packs", "painted-wolf", "platform", "host", "summarize.yaml")
	raw, err := os.ReadFile(yamlPath)
	if err != nil {
		t.Fatalf("read summarize.yaml: %v", err)
	}
	yml := string(raw)
	for _, key := range []string{
		"subtree_fanout_max",
		"subtree_damping",
		"subtree_min_rollup_share_pct",
		"subtree_doclink_max",
		"subtree_fanin_max",
		"subtree_fanin_grep_max",
		"subtree_min_drills",
		"subtree_max_drills",
		"subtree_task_depth_alpha",
		"subtree_name_index_max",
		"next_actions_max",
	} {
		if !strings.Contains(yml, key+":") {
			t.Errorf("summarize.yaml missing %s", key)
		}
	}
	if !strings.Contains(yml, "prune_nested_vcs:") {
		t.Error("summarize.yaml missing prune_nested_vcs")
	}
	if !strings.Contains(yml, "file_chunk_bytes:") {
		t.Error("summarize.yaml missing file_chunk_bytes")
	}
}
