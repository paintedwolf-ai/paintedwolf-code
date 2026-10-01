package hitl

import (
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

// Each card kind parks direction in its own result shape, and the stamp is what
// carries it to the model — so reading one shape covers only some denials.
func TestRejectionGuidanceReadsEveryResultShape(t *testing.T) {
	for name, tc := range map[string]struct {
		row  StoredCheckpoint
		want string
	}{
		"tool approval": {
			row:  StoredCheckpoint{Result: &DecisionResult{Comments: " Use a longer value. "}},
			want: "Use a longer value.",
		},
		"content apply": {
			row:  StoredCheckpoint{ContentResult: &ContentApplyResolve{Guidance: " Keep the timeout at 5s. "}},
			want: "Keep the timeout at 5s.",
		},
		"neither": {row: StoredCheckpoint{}},
		"blank comments fall through to content": {
			row: StoredCheckpoint{
				Result:        &DecisionResult{Comments: "  "},
				ContentResult: &ContentApplyResolve{Guidance: "Only the abort signal."},
			},
			want: "Only the abort signal.",
		},
	} {
		if got := rejectionGuidance(tc.row); got != tc.want {
			t.Fatalf("%s: guidance = %q, want %q", name, got, tc.want)
		}
	}
}

// Guidance is direction attached to a No. Stamping an approval's comments would
// project them as instruction nobody attached to a denial.
func TestDecisionMetaCarriesGuidanceOnlyOnRejection(t *testing.T) {
	row := StoredCheckpoint{
		ID: "cp-1", Kind: api.CheckpointKindToolApproval,
		Result: &DecisionResult{Comments: "looks fine"},
	}
	if got := checkpointDecisionMeta(row, DecisionStatusApproved).Guidance; got != "" {
		t.Fatalf("approved decision carried guidance: %q", got)
	}
	if got := checkpointDecisionMeta(row, DecisionStatusRejected).Guidance; got != "looks fine" {
		t.Fatalf("rejected decision guidance = %q", got)
	}
}
