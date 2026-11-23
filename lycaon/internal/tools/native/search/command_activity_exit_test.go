//go:build unix

package search_test

import (
	"context"
	"encoding/json"
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
	"github.com/lycaon/lycaon/internal/tools/native/literal"
	"github.com/lycaon/lycaon/internal/tools/native/search"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestProgramActivitiesUseSearchPipelineExitSemantics(t *testing.T) {
	for _, tt := range []struct {
		name        string
		source      string
		status      int
		publishable bool
		failed      int
		nonzero     map[int]int
		closed      map[int]int
	}{
		{"comparison pipeline", "printf 'needle\\n' | grep absent", 1, true, 0, map[int]int{1: 2}, map[int]int{}},
		{"external and native closure", "external -- /usr/bin/yes y | grep y | head -1; external -- /bin/sh -c 'exit 1'", 1, true, 1, map[int]int{1: 1, 2: 1, 141: 1}, map[int]int{2: 1, 141: 1}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newActivityExitFixture(t)
			raw, err := f.Call(t, t.Context(), tt.source)
			testutil.FailErr(t, "run activity program", err)
			var outcome command.ProgramOutcome
			testutil.FailErr(t, "decode activity program", json.Unmarshal([]byte(raw), &outcome))
			if outcome.Status != tt.status || outcome.State != "completed" || outcome.Refusal != nil {
				t.Fatalf("unexpected program outcome: %+v", outcome)
			}
			page, err := f.Recorder.ReadProgramPage(t.Context(), f.Session, outcome.ProgramID, "", 20)
			testutil.FailErr(t, "read program after settlement", err)
			activityFailures := 0
			for _, activity := range page.Activities {
				activityFailures += activity.FailedCount

			}
			if len(page.Activities) == 0 || activityFailures != tt.failed {
				t.Fatalf("activities = %+v, want %d failed executions", page.Activities, tt.failed)
			}
			assertPipelineExitFacts(t, f, outcome, tt.publishable, tt.nonzero, tt.closed, page.Activities)
		})
	}
}

func newActivityExitFixture(t *testing.T) *programtest.Fixture {
	t.Helper()
	f := programtest.New(t, programtest.Options{Tools: []string{"grep"}})
	owners, err := search.NewOwners(native.ExecutionRevisions{Boundary: f.Boundary}, f.Recorder, nil)
	testutil.FailErr(t, "create native grep owner", err)
	literals, err := literal.NewOwners(native.ExecutionRevisions{Boundary: f.Boundary}, f.Recorder, literal.DomainOwners{})
	testutil.FailErr(t, "create native head owner", err)
	f.Runtime.Owners = func(_ tools.ToolContext, _ *projectpaths.ProgramPaths, memory *toolcontract.ProgramMemory, admission tooloutput.SpillAdmission) (command.ProgramOwners, error) {
		grep := *owners
		grep.Revisions.Memory, grep.Admission = memory, admission
		head := literals.ForProgram(memory)
		natives := map[string]command.NativeOperation{"head": {
			Tool: "read",
			Prepare: func(_ context.Context, paths *projectpaths.ProgramPaths, _ commandprogram.Dispatch, request commandprogram.OperationRequest) (command.NativePrepared, error) {
				prepared, err := head.Prepare(paths, request)
				if err != nil {
					return command.NativePrepared{}, err
				}
				return command.NativePrepared{Review: prepared.Review, Invoke: prepared.InvokeOwner}, nil
			},
		}, "grep": {
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

func assertPipelineExitFacts(t *testing.T, f *programtest.Fixture, outcome command.ProgramOutcome, publishable bool, nonzero, closed map[int]int, activities []api.CommandActivity) {
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
	rows, err := f.Database.QueryContext(t.Context(), `SELECT id,operation_id,status_code,publication_eligible,consumer_closed FROM invocation_program_executions WHERE program_id=?`, outcome.ProgramID)
	testutil.FailErr(t, "read raw pipeline facts", err)
	defer func() { testutil.FailErr(t, "close receipt rows", rows.Close()) }()
	foundPipeline, observedClosed := false, map[int]int{}
	for rows.Next() {
		var id, operation string
		var status *int
		var eligible, consumerClosed bool
		testutil.FailErr(t, "scan raw pipeline facts", rows.Scan(&id, &operation, &status, &eligible, &consumerClosed))
		if operation == "program |" {
			foundPipeline = true
			for _, activity := range activities {
				if activity.ID == id {
					t.Fatalf("pipeline aggregate duplicated a stage: %+v", activity)
				}
			}
			if eligible != publishable {
				t.Fatalf("pipeline publication eligibility = %t, want %t", eligible, publishable)
			}
		}
		if consumerClosed && status != nil {
			observedClosed[*status]++
		}
	}
	testutil.FailErr(t, "iterate raw pipeline facts", rows.Err())
	if !foundPipeline || !reflect.DeepEqual(observedClosed, closed) {
		t.Fatalf("raw closure statuses = %v, want %v, pipeline=%t", observedClosed, closed, foundPipeline)
	}
}
