package toolexecution

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/platform"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

// stageMemory is the program's aggregate ledger with the given state capacity.
func stageMemory(t testing.TB, state int64) *toolcontract.ProgramMemory {
	t.Helper()
	memory, err := toolcontract.NewProgramMemory(toolcontract.ProgramMemoryLimits{Working: 32 << 20, State: state})
	testutil.FailErr(t, "program memory", err)
	return memory
}

// exhaustState holds the remaining state capacity until the release is called.
func exhaustState(t testing.TB, memory *toolcontract.ProgramMemory, state int64) func() {
	t.Helper()
	release, err := memory.ReserveProgramState(t.Context(), state-memory.InUse().State)
	testutil.FailErr(t, "exhaust program state", err)
	return release
}

func stateRefusal(err error) bool {
	var limit *toolcontract.ProgramMemoryLimitError
	return errors.As(err, &limit) && limit.Account == "state"
}

func TestNativeStageSnapshotAdmitsBeforeRetentionAndHoldsThroughProjection(t *testing.T) {
	stages := []toolcontract.ExecutionStage{{ExecutionID: "child", OperationID: "read", Argv: []string{"cat", "fixture input"}, CommandLine: "cat 'fixture input'", PipelineCommandLine: "cat 'fixture input'"}}
	if _, err := PrepareNativeStages(t.Context(), stageMemory(t, 1), stages); !stateRefusal(err) {
		t.Fatalf("reservation refusal lost: %v", err)
	}
	admission := stageMemory(t, 64<<20)
	snapshot, err := PrepareNativeStages(t.Context(), admission, stages)
	testutil.FailErr(t, "reserve native facts", err)
	if admission.InUse().State <= int64(len(stages[0].Argv[1])) {
		t.Fatal("retained stage projection has no aggregate reservation")
	}
	stages[0].Argv[1] = "changed after capture"
	projection, err := snapshot.PolicyContext(platform.PolicyContext{ToolName: "command", ActionID: "actual-model-call"})
	testutil.FailErr(t, "project admitted native facts", err)
	if projection.BoundStages[0].Argv[1] != "fixture input" || projection.ActionID != "actual-model-call" || admission.InUse().State == 0 {
		t.Fatal("review facts changed or reservation released before projection")
	}
	if _, err := snapshot.PolicyContext(platform.PolicyContext{}); err == nil {
		t.Fatal("unbounded repeated retained projection admitted")
	}
	snapshot.Close()
	snapshot.Close()
	if admission.InUse().State != 0 {
		t.Fatalf("state lease not settled once: %+v", admission.InUse())
	}
	if _, err := snapshot.PolicyContext(platform.PolicyContext{}); err == nil {
		t.Fatal("closed snapshot reused")
	}
}

func TestNativeStageSnapshotRefusesMissingFactsAndCancellation(t *testing.T) {
	admission := stageMemory(t, 64<<20)
	stages := []toolcontract.ExecutionStage{{ExecutionID: "child", OperationID: "read", Argv: []string{"cat"}}}
	if _, err := PrepareNativeStages(t.Context(), nil, stages); err == nil {
		t.Fatal("unmetered retention admitted")
	}
	if _, err := PrepareNativeStages(t.Context(), admission, []toolcontract.ExecutionStage{{}}); err == nil {
		t.Fatal("missing bound facts admitted")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := PrepareNativeStages(ctx, admission, stages); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled reservation: %v", err)
	}
	if admission.InUse().State != 0 {
		t.Fatal("refused snapshot retained state")
	}
}
