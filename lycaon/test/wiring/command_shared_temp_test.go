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
	for _, tc := range []struct {
		name, program, answer, want string
		posture                     gate.Posture
	}{
		{"decline", "mkdir producer-started", "decline", "original", gate.PostureLight},
		{"allow", "printf changed", "recommended", "changed", gate.PostureBalanced},
	} {
		t.Run(tc.name, func(t *testing.T) {
			shared := outsideCommandFolder(t)
			target := filepath.Join(shared, "note.txt")
			testutil.FailErr(t, "seed redirect target", os.WriteFile(target, []byte("original"), 0600))
			session := newCommandProgramSession(t, tc.program+" > '"+target+"'", "", tc.posture)
			t.Setenv("TMPDIR", shared)
			cards := runCommandWithApprovals(t, session, tc.answer)
			plan := cards[0].ToolApproval.Plan
			if plan.Subject.Kind != wire.ApprovalSubjectKindWriteRootSet || !slices.Contains(plan.Reasons, wire.GateOutsideRootsWrite) {
				t.Fatalf("shared temp review = %+v", plan)
			}
			data, err := os.ReadFile(target)
			testutil.FailErr(t, "read reviewed target", err)
			if string(data) != tc.want {
				t.Fatalf("redirect target = %q, want %q", data, tc.want)
			}
			result := session.outcome(t, "program-1")
			if (result.Status == 0) != (tc.name == "allow") {
				t.Fatalf("redirect result = %+v", result)
			}
			if tc.name == "decline" {
				if _, err := os.Stat(filepath.Join(session.dir, "producer-started")); !os.IsNotExist(err) {
					t.Fatalf("declined producer started: %v", err)
				}
			}
		})
	}
}
