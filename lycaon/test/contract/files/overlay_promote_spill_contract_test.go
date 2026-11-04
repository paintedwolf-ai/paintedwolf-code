package contract

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/tooloutput"
	"github.com/lycaon/lycaon/test/contract/internal/catalogfixture"
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

func TestOverlayPromoteHintCodesRegistered(t *testing.T) {
	t.Parallel()
	for _, code := range []string{"BANNER_PROMOTE_HUNK_PICK", "BANNER_PROMOTE_PARTIAL_PATH", "BANNER_PROMOTE_SPILL_FIRST", "BANNER_PROMOTE_OVERLAY_BODY", "BANNER_OVERLAY_MERGE_PLAN", "BANNER_PROMOTE_SCOPED_HUNKS", "BANNER_PROMOTE_HIGH_CONFLICT"} {
		catalogfixture.FindStockPolicyFile(t, code)
	}
}
