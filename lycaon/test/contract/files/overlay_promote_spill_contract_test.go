package contract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/tooloutput"
	"github.com/lycaon/lycaon/test/contract/internal/catalogfixture"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestOverlayPromoteSpillUsesProjectRelativePath(t *testing.T) {
	t.Parallel()
	rel := tooloutput.PromoteSpillRelPath("job-abc")
	if !strings.HasPrefix(rel, tooloutput.PromoteSpillDir+"/") {
		t.Fatalf("rel=%q", rel)
	}
	if strings.Contains(rel, "..") {
		t.Fatalf("rel must stay in project: %q", rel)
	}
}

func TestOverlayPromoteHintCodeRegistered(t *testing.T) {
	t.Parallel()
	catalogfixture.FindStockPolicyFile(t, "OVERLAY_PROMOTE_SPILL")
}

func TestPreviewOverlaySchemaIncludesPathAndSpillDocs(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, "lycaon", "config", "packs", "painted-wolf", "platform", "tools", "schemas", "preview_overlay.yaml"))
	contractcheck.FailErr(t, "read file", err)
	text := string(raw)
	for _, needle := range []string{"path:", "spill_path", "promote-spills"} {
		if !strings.Contains(text, needle) {
			t.Fatalf("preview_overlay schema missing %q", needle)
		}
	}
}

func TestPromoteOverlaySchemaTeachesPlainMergeVerbs(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, "lycaon", "config", "packs", "painted-wolf", "platform", "tools", "schemas", "promote_overlay.yaml"))
	contractcheck.FailErr(t, "read file", err)
	text := string(raw)
	for _, needle := range []string{
		"keep_both",
		"keep_theirs",
		"keep_ours",
		"drop",
		"resolutions",
	} {
		if !strings.Contains(text, needle) {
			t.Fatalf("promote_overlay schema missing %q", needle)
		}
	}
}

func TestCoordinatorOverlayPromptTeachesMergeVerbs(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, "lycaon", "config", "packs", "painted-wolf", "platform", "shared", "partials", "coordinator-overlay-promote.md"))
	contractcheck.FailErr(t, "read file", err)
	text := string(raw)
	for _, needle := range []string{
		"promote_overlay",
		"preview_overlay",
		"reject_overlay",
		"promote_sequence",
		"keep_both",
		"keep_theirs",
		"keep_ours",
		"drop",
	} {
		if !strings.Contains(text, needle) {
			t.Fatalf("coordinator-overlay-promote.md missing %q", needle)
		}
	}
}

func TestOverlayPromoteHintCodesRegistered(t *testing.T) {
	t.Parallel()
	for _, code := range []string{"BANNER_PROMOTE_HUNK_PICK", "BANNER_PROMOTE_PARTIAL_PATH", "BANNER_PROMOTE_SPILL_FIRST", "BANNER_PROMOTE_OVERLAY_BODY", "BANNER_OVERLAY_MERGE_PLAN", "BANNER_PROMOTE_SCOPED_HUNKS", "BANNER_PROMOTE_HIGH_CONFLICT"} {
		catalogfixture.FindStockPolicyFile(t, code)
	}
}
