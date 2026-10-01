package gate_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestReasonSegmentsSplitsCompositeKey(t *testing.T) {
	t.Parallel()
	d := &gate.Decision{
		Primary:   api.GateAuthorityMisuse,
		Also:      []api.ApprovalGate{api.GateOutsideRootsWrite},
		ReasonKey: "authority_misuse:aws-cli/s3-remove-bucket|outside_roots_write:write",
	}
	got := d.ReasonSegments()
	want := []string{
		"authority_misuse:aws-cli/s3-remove-bucket",
		"outside_roots_write:write",
	}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v want %v", got, want)
		}
	}
	if (&gate.Decision{}).ReasonSegments() != nil {
		t.Fatal("empty ReasonKey should yield nil segments")
	}
}
