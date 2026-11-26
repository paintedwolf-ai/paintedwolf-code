package commands

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/testutil"
)

// An approved input redirect outside the attached folders is a read the chat records.
func TestReviewedInputRedirectIsARecordedRead(t *testing.T) {
	outside := outsideCommandFolder(t)
	file := filepath.Join(outside, "private.txt")
	testutil.FailErr(t, "seed outside input", os.WriteFile(file, []byte("one\ntwo\n"), 0o600))
	s := newProgramHost(t, gate.PostureBalanced).program(t, "wc -l < '"+file+"'", "")
	runCommandApprovingAll(t, s)
	if outcome := s.outcome(t, "program-1"); outcome.Status != 0 || strings.TrimSpace(outcome.Stdout) != "2" {
		t.Fatalf("reviewed input = %+v", outcome)
	}
	if read := s.readFiles(t); read != 1 {
		t.Fatalf("chat file reads = %d, want 1", read)
	}
	if anchors := s.locationAnchors(t); !slices.Contains(anchors, outsideAnchorOf(t, outside)) {
		t.Fatalf("chat locations = %v, want %s", anchors, outside)
	}
}
