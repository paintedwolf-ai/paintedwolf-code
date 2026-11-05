package toolexecution

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
)

type fixedMutationProgress struct{ state progress.PrerequisiteState }

func (f fixedMutationProgress) ProgramProgressState(context.Context, string) (progress.PrerequisiteState, error) {
	return f.state, nil
}

func TestReachedMutationProgressUsesResolvedProjectTarget(t *testing.T) {
	invocation, _ := frameFixture(t)
	definition := nativeDefinition(t, "write")
	state := progress.PrerequisiteState{SessionID: invocation.SessionID, RootSessionID: invocation.SessionID}
	provider := fixedMutationProgress{state}
	testutil.FailErr(t, "scratch write", checkReachedMutationProgress(t.Context(), provider, definition, invocation, "", "/tmp/output"))
	err := checkReachedMutationProgress(t.Context(), provider, definition, invocation, "r1", "/project/config.json")
	var refusal *toolrejection.ToolReject
	if !errors.As(err, &refusal) || refusal.Code != "PROGRESS_MISSING" || refusal.Data["path"] != "/project/config.json" {
		t.Fatalf("project write refusal = %v", err)
	}
	provider.state.Content = "- [ ] Update config"
	testutil.FailErr(t, "planned project write", checkReachedMutationProgress(t.Context(), provider, definition, invocation, "r1", "/project/config.json"))
}

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

func TestOperationProgressRejectsCrossSessionState(t *testing.T) {
	invocation, _ := frameFixture(t)
	definition := nativeDefinition(t, "write")
	state := progress.PrerequisiteState{SessionID: invocation.SessionID, RootSessionID: "root-session", Content: "- [ ] Actual task"}
	testutil.FailErr(t, "matching actual state", CheckOperationProgressState(definition, invocation, state))
	state.SessionID = "foreign-session"
	if CheckOperationProgressState(definition, invocation, state) == nil {
		t.Fatal("foreign progress state admitted")
	}
	state.SessionID, state.RootSessionID = invocation.SessionID, ""
	if CheckOperationProgressState(definition, invocation, state) == nil {
		t.Fatal("missing progress root admitted")
	}
}

func TestReachedProgressGateRefreshesFinishObligationsBeforeMutation(t *testing.T) {
	invocation, _ := frameFixture(t)
	definition := nativeDefinition(t, "write")
	provider := fixedMutationProgress{progress.PrerequisiteState{SessionID: invocation.SessionID, RootSessionID: invocation.SessionID, Content: "- [ ] Deliver"}}
	testutil.FailErr(t, "admit planned operation", CheckOperationProgressState(definition, invocation, provider.state))
	provider.state.Reconciliation = &progress.Reconciliation{Token: "new-finish", Jobs: []string{"worker-one"}}
	err := checkReachedMutationProgress(t.Context(), provider, definition, invocation, "project-root", "/project/result.txt")
	var refused *toolrejection.ToolReject
	if !errors.As(err, &refused) || refused.Code != "PROGRESS_RECONCILIATION_REQUIRED" {
		t.Fatalf("late finish bypassed write-door refresh: %v", err)
	}
	if refused.OwnerRef != "" || refused.Data["root_session_id"] != provider.state.RootSessionID {
		t.Fatalf("prerequisite category replaced operation resource attribution: %+v", refused)
	}
	for _, name := range []string{"read", "delete"} {
		testutil.FailErr(t, "preserve ungated operation", CheckOperationProgressState(nativeDefinition(t, name), invocation, provider.state))
	}
	provider.state.Reconciliation = nil
	testutil.FailErr(t, "admit acknowledged write", checkReachedMutationProgress(t.Context(), provider, definition, invocation, "project-root", "/project/result.txt"))
}
