package toolexecution

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/platform"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

type stageAdmissionFixture struct {
	err      error
	held     int64
	released int
}

func (f *stageAdmissionFixture) ReserveProgramState(_ context.Context, n int64) (func(), error) {
	if f.err != nil {
		return nil, f.err
	}
	f.held += n
	return func() { f.held -= n; f.released++ }, nil
}

func TestNativeStageSnapshotAdmitsBeforeRetentionAndHoldsThroughProjection(t *testing.T) {
	stages := []toolcontract.ExecutionStage{{ExecutionID: "child", OperationID: "read", Argv: []string{"cat", "fixture input"}, CommandLine: "cat 'fixture input'", PipelineCommandLine: "cat 'fixture input'"}}
	denied := errors.New("aggregate state exhausted")
	if _, err := PrepareNativeStages(t.Context(), &stageAdmissionFixture{err: denied}, stages); !errors.Is(err, denied) {
		t.Fatalf("reservation refusal lost: %v", err)
	}
	admission := &stageAdmissionFixture{}
	snapshot, err := PrepareNativeStages(t.Context(), admission, stages)
	testutil.FailErr(t, "reserve native facts", err)
	if admission.held <= int64(len(stages[0].Argv[1])) {
		t.Fatal("retained stage projection has no aggregate reservation")
	}
	stages[0].Argv[1] = "changed after capture"
	projection, err := snapshot.PolicyContext(platform.PolicyContext{ToolName: "command", ActionID: "actual-model-call"})
	testutil.FailErr(t, "project admitted native facts", err)
	if projection.BoundStages[0].Argv[1] != "fixture input" || projection.ActionID != "actual-model-call" || admission.held == 0 {
		t.Fatal("review facts changed or reservation released before projection")
	}
	if _, err := snapshot.PolicyContext(platform.PolicyContext{}); err == nil {
		t.Fatal("unbounded repeated retained projection admitted")
	}
	snapshot.Close()
	snapshot.Close()
	if admission.held != 0 || admission.released != 1 {
		t.Fatalf("state lease not settled once: %+v", admission)
	}
	if _, err := snapshot.PolicyContext(platform.PolicyContext{}); err == nil {
		t.Fatal("closed snapshot reused")
	}
}

func TestNativeStageSnapshotRefusesMissingFactsAndCancellation(t *testing.T) {
	admission := &stageAdmissionFixture{}
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
	if admission.held != 0 {
		t.Fatal("refused snapshot retained state")
	}
}
