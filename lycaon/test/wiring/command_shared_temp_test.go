package wiring

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestDeclaredSharedTempRedirectIsReviewedBeforeWriting(t *testing.T) {
	shared := outsideCommandFolder(t)
	t.Setenv("TMPDIR", shared)
	target := filepath.Join(shared, "note.txt")
	testutil.FailErr(t, "seed redirect target", os.WriteFile(target, []byte("original"), 0600))
	session := newCommandProgramSession(t, "printf changed > '"+target+"'", "", gate.PostureBalanced)
	cards := runCommandWithApprovals(t, session, "decline")
	if !slices.Contains(cards[0].ToolApproval.Plan.Reasons, wire.GateOutsideRootsWrite) {
		t.Fatalf("redirect review = %+v", cards[0].ToolApproval.Plan)
	}
	data, err := os.ReadFile(target)
	testutil.FailErr(t, "read declined target", err)
	if string(data) != "original" {
		t.Fatalf("declined redirect wrote %q", data)
	}
	if result := session.outcome(t, "program-1"); result.Status == 0 {
		t.Fatalf("declined redirect passed: %+v", result)
	}
}
