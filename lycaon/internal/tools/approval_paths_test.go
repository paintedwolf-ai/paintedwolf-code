package tools

import (
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/projectroot"
)

func TestApprovalResolutionPreservesUnresolvedTargets(t *testing.T) {
	root := t.TempDir()
	tc := ToolContext{Roots: []projectroot.RootRef{{ID: "root", Label: "folder", Path: root, IsPrimary: true}}, ActiveRootID: "root"}
	for _, worker := range []bool{false, true} {
		if worker {
			tc.WorkerBranchRoot = t.TempDir()
		}
		for _, invalid := range []string{"../escape.txt", "@missing/file.txt", "nul\x00path"} {
			args := map[string]any{"paths": []string{"valid.txt", invalid}}
			got := resolvedApprovalFiles("copy", args, tc)
			if len(got) != 2 || !filepath.IsAbs(got[0]) || got[1] != "" {
				t.Fatalf("worker=%t invalid=%q: partial resolution %v", worker, invalid, got)
			}
		}
	}
}
