package command

import (
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

func TestRecordedProgramSetupFailureReleasesPlanOnce(t *testing.T) {
	memory, err := toolcontract.NewProgramMemory(toolcontract.ProgramMemoryLimits{Working: 32 << 20, State: 64 << 20})
	testutil.FailErr(t, "admit plan", err)
	parsed, snapshot, err := parseProgram(t.Context(), memory, "true")
	testutil.FailErr(t, "parse actual plan", err)
	setupErr := errors.New("utility setup failed")
	owners := ProgramOwners{Builtins: func(BuiltinPorts) (map[string]commandprogram.OperationDispatcher, func(), error) {
		return nil, nil, setupErr
	}}
	released := 0
	release := func() { released++; _ = snapshot.Close(); _ = parsed.Close() }
	runtime := &ProgramRuntime{}
	_, err = runtime.executeRecordedProgram(t.Context(), &programRun{memory: memory}, owners, nil, nil, snapshot.Plan, release, nil)
	if !errors.Is(err, setupErr) || released != 1 {
		t.Fatalf("setup error=%v release count=%d", err, released)
	}
	if used := memory.InUse(); used.Working != 0 || used.State != 0 {
		t.Fatalf("setup retained plan memory: %+v", used)
	}
}
