package contract

import (
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/internal/hitl"
)

func TestCheckpointManagerHasNoPatchPendingToolApprovalRepeat(t *testing.T) {
	t.Parallel()
	iface := reflect.TypeOf((*hitl.CheckpointManager)(nil)).Elem()
	if _, ok := iface.MethodByName("PatchPendingToolApprovalRepeat"); ok {
		t.Fatal("CheckpointManager must not expose PatchPendingToolApprovalRepeat — repeat is frozen at mint")
	}
}
