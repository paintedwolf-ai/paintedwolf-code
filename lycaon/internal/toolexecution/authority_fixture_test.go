package toolexecution

import (
	"testing"

	"github.com/lycaon/lycaon/internal/hitl"
)

func assertDirectAuthorityKinds(t *testing.T, deltas []hitl.ApprovalAuthorityDelta, want ...hitl.AuthorityDeltaKind) {
	t.Helper()
	if len(deltas) != len(want) {
		t.Fatalf("authority count = %d, want %d: %+v", len(deltas), len(want), deltas)
	}
	for i := range want {
		if deltas[i].Kind != want[i] {
			t.Fatalf("authority[%d] = %q, want %q", i, deltas[i].Kind, want[i])
		}
	}
}
