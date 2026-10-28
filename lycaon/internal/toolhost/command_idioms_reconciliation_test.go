package toolhost

import (
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestCommandArrayAssignmentsSurviveTheHostEntry(t *testing.T) {
	f := newOutputDeliveryFixture(t)
	raw, err := f.Call(t, t.Context(), `arr=(alpha "beta gamma" delta); arr[1]="BETA GAMMA"; echo "${arr[0]}:${arr[1]}:${#arr[@]}"`)
	testutil.FailErr(t, "run array assignment", err)
	out := decodeProgramOutcome(t, raw)
	if out.State != "completed" || out.Status != 0 || out.Stdout != "alpha:BETA GAMMA:3\n" {
		t.Fatalf("array assignment outcome = %+v", out)
	}
}
