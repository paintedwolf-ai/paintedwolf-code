package command_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/programtest"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/command"
	"github.com/lycaon/lycaon/pkg/api"
)

type verifyRunRecorder struct {
	tool string
	run  tools.SourceRunCapture
}

func (r *verifyRunRecorder) RecordSourceRunEvidence(_ context.Context, _ string, _ *api.Session, tool string, run tools.SourceRunCapture) {
	r.tool, r.run = tool, run
}

func TestVerifyProgramParkRetainsIdentityAndSettledCheck(t *testing.T) {
	program := programtest.NewWaitingProgram(programtest.NewManualClock(), programtest.SleepState(120*time.Second))
	f := newParkFixture(t, program)
	runs := &verifyRunRecorder{}
	f.Runtime.SourceRuns = runs
	raw, err := f.Invoke(t, t.Context(), "verify", map[string]any{"command": "sleep 120"}, func(ctx context.Context, args map[string]any, tc tools.ToolContext) (string, error) {
		tc.Execution.VerificationCheck = true
		return f.Runtime.Run(ctx, "verify", args, tc)
	})
	testutil.FailErr(t, "park verification", err)
	var parked command.ParkedProgramResult
	testutil.FailErr(t, "decode parked verification", json.Unmarshal([]byte(raw), &parked))
	recorded := <-f.parking.parked
	if parked.ResultVersion != 2 || f.out.SourceRun != nil {
		t.Fatalf("premature verification result %s %+v", raw, f.out.SourceRun)
	}
	program.EndWait()
	result := f.settledProgram(t, recorded)
	if result.ResultVersion != 2 || result.Outcome != api.SourceVerdictPassed || runs.tool != "verify" || !runs.run.IsCheck {
		t.Fatalf("settled=%+v tool=%s run=%+v", result, runs.tool, runs.run)
	}
}
