package toolrejection

import (
	"testing"

	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

// A coordinator read the sandbox refuses carries its rejection code whichever tool attempted it.
func TestCoordinatorReadOutsideScopeIsRejectedForAnyTool(t *testing.T) {
	err := &sandbox.ScopeError{Path: "../elsewhere/notes.txt", Profile: toolcontract.CoordinatorProfileID, Kind: sandbox.ScopeRead}
	for _, tool := range []string{"command", "summarize"} {
		reject := ScopeObservation(toolcontract.CoordinatorProfileID, "", tool, "", err)
		if reject == nil || reject.Code != "COORDINATOR_READ_OUTSIDE_SCOPE" || reject.Data["path"] != err.Path {
			t.Fatalf("%s: reject = %+v, want COORDINATOR_READ_OUTSIDE_SCOPE for %s", tool, reject, err.Path)
		}
	}
	if reject := ScopeObservation("worker", "", "command", "", err); reject != nil {
		t.Fatalf("non-coordinator profile got %+v, want no coordinator rejection", reject)
	}
}
