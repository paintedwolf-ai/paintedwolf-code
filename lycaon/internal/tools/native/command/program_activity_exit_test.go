//go:build unix

package command_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/programtest"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tooloutput"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
	"github.com/lycaon/lycaon/internal/tools/native/command"
	"github.com/lycaon/lycaon/internal/tools/native/search"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

func TestProgramActivitiesUseSettledExitSemantics(t *testing.T) {
	for _, tt := range []struct {
		name        string
		source      string
		status      int
		pipeline    bool
		publishable bool
		failed      int
		nonzero     map[int]int
		closed      map[int]int
	}{
		{"comparison pipeline", "printf 'needle\\n' | grep absent", 1, true, true, 0, map[int]int{1: 2}, map[int]int{}},
		{"external and native closure", "yes | grep y | head -1; external -- /bin/sh -c 'exit 1'", 1, true, true, 1, map[int]int{1: 1, 2: 1, 141: 1}, map[int]int{2: 1, 141: 1}},
		{"unhandled pipeline", "external -- /bin/sh -c 'exit 4' | true", 4, true, false, 1, map[int]int{4: 2}, map[int]int{}},
		{"conditional recovery", "external -- /bin/sh -c 'exit 1' || true", 0, false, false, 0, map[int]int{1: 1}, map[int]int{}},
		{"sequence recovery", "external -- /bin/sh -c 'exit 1'; true", 0, false, false, 0, map[int]int{1: 1}, map[int]int{}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newActivityExitFixture(t)
			raw, err := f.Call(t, t.Context(), tt.source)
			testutil.FailErr(t, "run activity program", err)
			outcome := decodeOutcome(t, raw)
			if outcome.Status != tt.status || outcome.State != "completed" || outcome.Refusal != nil {
				t.Fatalf("unexpected program outcome: %+v", outcome)
			}
			page, err := f.Recorder.ReadProgramPage(t.Context(), f.Session, outcome.ProgramID, "", 20)
			testutil.FailErr(t, "read program after settlement", err)
			activityFailures := 0
			for _, activity := range page.Activities {
				activityFailures += activity.FailedCount
				for _, execution := range page.Executions {
					if execution.ID == activity.ID && execution.NativeCommand == "|" {
						t.Fatalf("pipeline aggregate duplicated a stage: %+v", activity)
					}
				}
			}
			if len(page.Activities) == 0 || activityFailures != tt.failed {
				t.Fatalf("activities = %+v, want %d failed executions", page.Activities, tt.failed)
			}
			assertPipelineExitFacts(t, f, outcome, tt.pipeline, tt.publishable, tt.nonzero, tt.closed)
		})
	}
}

func newActivityExitFixture(t *testing.T) *programtest.Fixture {
	t.Helper()
	f := programtest.New(t, programtest.Options{Tools: []string{"grep"}})
	owners, err := search.NewOwners(native.ExecutionRevisions{Boundary: f.Boundary}, f.Recorder, nil)
	testutil.FailErr(t, "create native grep owner", err)
	f.Runtime.Owners = func(_ tools.ToolContext, _ *projectpaths.ProgramPaths, memory *toolcontract.ProgramMemory, admission tooloutput.SpillAdmission) (command.ProgramOwners, error) {
		grep := *owners
		grep.Revisions.Memory, grep.Admission = memory, admission
		natives := map[string]command.NativeOperation{"head": firstLineOperation(), "grep": {
			Tool: "grep",
			Prepare: func(_ context.Context, paths *projectpaths.ProgramPaths, dispatch commandprogram.Dispatch, request commandprogram.OperationRequest) (command.NativePrepared, error) {
				prepared, err := grep.Prepare(paths, dispatch, request)
				if err != nil {
					return command.NativePrepared{}, err
				}
				return command.NativePrepared{Mode: search.EffectMode(prepared.Argv), Review: prepared.Review, Invoke: prepared.InvokeOwner}, nil
			},
		}}
		return command.ProgramOwners{Natives: natives}, nil
	}
	return f
}

func assertPipelineExitFacts(t *testing.T, f *programtest.Fixture, outcome command.ProgramOutcome, hasPipeline, publishable bool, nonzero, closed map[int]int) {
	t.Helper()
	observedNonzero := map[int]int{}
	for _, execution := range outcome.NonzeroExecutions {
		if execution.StatusCode != nil && *execution.StatusCode != 0 {
			observedNonzero[*execution.StatusCode]++
		}
	}
	if !reflect.DeepEqual(observedNonzero, nonzero) {
		t.Fatalf("model receipts for %s = %v, want %v", outcome.ProgramID, observedNonzero, nonzero)
	}
	rows, err := f.Database.QueryContext(t.Context(), `SELECT operation_id,status_code,publication_eligible,consumer_closed FROM invocation_program_executions WHERE program_id=?`, outcome.ProgramID)
	testutil.FailErr(t, "read raw pipeline facts", err)
	defer func() { testutil.FailErr(t, "close receipt rows", rows.Close()) }()
	foundPipeline, observedClosed := false, map[int]int{}
	for rows.Next() {
		var operation string
		var status *int
		var eligible, consumerClosed bool
		testutil.FailErr(t, "scan raw pipeline facts", rows.Scan(&operation, &status, &eligible, &consumerClosed))
		if operation == "program |" {
			foundPipeline = true
			if eligible != publishable {
				t.Fatalf("pipeline publication eligibility = %t, want %t", eligible, publishable)
			}
		}
		if consumerClosed && status != nil {
			observedClosed[*status]++
		}
	}
	testutil.FailErr(t, "iterate raw pipeline facts", rows.Err())
	if foundPipeline != hasPipeline || !reflect.DeepEqual(observedClosed, closed) {
		t.Fatalf("raw closure statuses = %v, want %v, pipeline=%t", observedClosed, closed, foundPipeline)
	}
}

func TestProgramActivitiesUseFinalStatusForUnrecordedControl(t *testing.T) {
	for _, tt := range []struct {
		source, activityState string
	}{
		{"false", "failed"},
		{"false || true", "completed"},
	} {
		t.Run(tt.source, func(t *testing.T) {
			f := newProgramFixture(t)
			raw, err := f.call(t, t.Context(), tt.source)
			testutil.FailErr(t, "run local control program", err)
			outcome := decodeOutcome(t, raw)
			page, err := f.Recorder.ReadProgramPage(t.Context(), f.Session, outcome.ProgramID, "", 10)
			testutil.FailErr(t, "read local control activities", err)
			if len(page.Executions) != 0 || len(page.Activities) != 1 || page.Activities[0].State != tt.activityState {
				t.Fatalf("local control projected as %+v with %d execution rows", page.Activities, len(page.Executions))
			}
		})
	}
}
