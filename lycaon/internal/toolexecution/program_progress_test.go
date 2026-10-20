package toolexecution

import (
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
)

func TestOperationProgressUsesMetadataAndExistingExemptions(t *testing.T) {
	invocation, _ := frameFixture(t)
	write := nativeDefinition(t, "write")
	for _, policy := range []toolcontract.ProgressPolicy{toolcontract.ProgressExistingAdmission, toolcontract.ProgressRequired, toolcontract.ProgressNotRequired} {
		facts := write.Facts()
		facts.ProgressGate = policy
		definition, err := toolcontract.NewOperationDefinition(facts)
		testutil.FailErr(t, "freeze progress metadata", err)
		err = CheckOperationProgress(definition, invocation, "", false, false)
		var refusal *toolrejection.ToolReject
		if policy != toolcontract.ProgressNotRequired {
			if !errors.As(err, &refusal) || refusal.Code != "PROGRESS_MISSING" || refusal.OwnerRef != facts.Contract.Owner || refusal.Data["source_line"] != 1 {
				t.Fatalf("lost actual progress refusal: %v", err)
			}
		} else if err != nil {
			t.Fatalf("ungated operation refused: %v", err)
		}
		for _, exemption := range []struct{ worker, review bool }{{true, false}, {false, true}} {
			testutil.FailErr(t, "existing workflow exemption", CheckOperationProgress(definition, invocation, "", exemption.worker, exemption.review))
		}
		testutil.FailErr(t, "existing valid progress", CheckOperationProgress(definition, invocation, "- [ ] Inspect source", false, false))
	}
	testutil.FailErr(t, "read-only operation has no broad command gate", CheckOperationProgress(nativeDefinition(t, "read"), invocation, "", false, false))
	if CheckOperationProgress(toolcontract.OperationDefinition{}, invocation, "", false, false) == nil {
		t.Fatal("missing operation metadata admitted")
	}
}
