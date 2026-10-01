package compaction

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestIsOverlayPromoteConflictProtected(t *testing.T) {
	payload := `{"job_id":"job-1","conflict_digest":[{"path":"a.go","summary":["L1-2"]}]}`
	if !IsOverlayPromoteConflictProtected(payload) {
		t.Fatal("expected protected preview payload")
	}
	if IsOverlayPromoteConflictProtected(`{"job_id":"job-1","clean_paths":["a.go"]}`) {
		t.Fatal("clean-only preview should not be protected")
	}
}

func TestIsOverlayPromoteConflictProtected_handlesQuotedBraces(t *testing.T) {
	payload := `{"job_id":"job-1","conflict_digest":[{"path":"a.go","summary":["literal } brace"]}]}`
	if !IsOverlayPromoteConflictProtected(payload) {
		t.Fatal("quoted brace must not terminate the merge payload")
	}
}

func TestTrimOverlayPromoteChunkDropsDigestSummariesKeepsSpill(t *testing.T) {
	in := api.WorkerMergeResult{
		JobID:      "job-1",
		Mode:       "preview",
		SpillPath:  "promote-spills/job-1.json",
		CleanPaths: []string{"util.go"},
		PathStatus: []api.WorkerPromotePathStatus{
			{Path: "util.go", Status: api.WorkerPromotePathOutcomeClean},
			{Path: "router.go", Status: api.WorkerPromotePathOutcomeConflict, HunkCount: 4},
		},
		ConflictDigest: []api.WorkerPromoteConflictDigest{{
			Path:        "router.go",
			Summary:     []string{"L10-12: primary: p | branch: b", "L20-22: primary: x | branch: y"},
			BranchDelta: "+ branch line\n- primary line",
		}},
	}
	raw, err := json.Marshal(in)
	testutil.FailErr(t, "json.Marshal failed", err)
	banners := "\n>>> promote conflict digest\nCode: BANNER_PROMOTE_CONFLICT_DIGEST"
	out, ok := TrimOverlayPromoteChunk(string(raw) + banners)
	if !ok {
		t.Fatal("expected compact")
	}
	if strings.Contains(out, "L10-12") {
		t.Fatalf("digest summary lines must be dropped: %q", out[:200])
	}
	if !strings.Contains(out, "branch line") {
		t.Fatalf("branch_delta must be preserved in %q", out)
	}
	if !strings.Contains(out, "promote-spills/job-1.json") {
		t.Fatalf("missing spill_path in %q", out)
	}
	if !strings.Contains(out, "util.go") || !strings.Contains(out, "router.go") {
		t.Fatalf("missing path_status paths in %q", out)
	}
	if !strings.Contains(out, "BANNER_PROMOTE_CONFLICT_DIGEST") {
		t.Fatalf("guidance banners must be preserved: %q", out[len(out)-120:])
	}
}
