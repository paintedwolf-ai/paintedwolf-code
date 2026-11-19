//go:build unix

package command_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestProgramActivitiesUseSettledForegroundExits(t *testing.T) {
	for _, tt := range []struct {
		name, source   string
		status, failed int
		nonzero        int
	}{
		{"unhandled pipeline", "external -- /bin/sh -c 'exit 4' | true", 4, 1, 2},
		{"conditional recovery", "external -- /bin/sh -c 'exit 1' || true", 0, 0, 1},
		{"sequence recovery", "external -- /bin/sh -c 'exit 1'; true", 0, 0, 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newProgramFixture(t)
			raw, err := f.call(t, t.Context(), tt.source)
			testutil.FailErr(t, "run activity program", err)
			outcome := decodeOutcome(t, raw)
			if outcome.Status != tt.status || outcome.State != "completed" || outcome.Refusal != nil || len(outcome.NonzeroExecutions) != tt.nonzero {
				t.Fatalf("unexpected program outcome: %+v", outcome)
			}
			summary, err := f.Recorder.ActivitySummary(t.Context(), f.Session, outcome.ProgramID)
			testutil.FailErr(t, "read activity summary", err)
			if len(summary.Activities) != 1 || summary.Activities[0].FailedCount != tt.failed {
				t.Fatalf("activities = %+v, want one row and %d failed executions", summary.Activities, tt.failed)
			}
		})
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
