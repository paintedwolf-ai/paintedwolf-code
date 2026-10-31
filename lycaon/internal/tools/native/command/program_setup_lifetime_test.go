package command_test

import (
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tooloutput"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/command"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

func TestRecordedProgramSetupFailureReleasesPlanOnce(t *testing.T) {
	fixture := newProgramFixture(t)
	previous := fixture.Runtime.Owners
	setupErr := errors.New("utility setup failed")
	released := 0
	var memory *toolcontract.ProgramMemory
	fixture.Runtime.Owners = func(tc tools.ToolContext, paths *projectpaths.ProgramPaths, admitted *toolcontract.ProgramMemory, spill tooloutput.SpillAdmission) (command.ProgramOwners, error) {
		memory = admitted
		owners, err := previous(tc, paths, admitted, spill)
		owners.Builtins = func(command.BuiltinPorts) (map[string]commandprogram.OperationDispatcher, func(), error) {
			return nil, func() { released++ }, setupErr
		}
		return owners, err
	}
	_, err := fixture.call(t, t.Context(), "true")
	if !errors.Is(err, setupErr) || released != 1 || memory == nil {
		t.Fatalf("setup error=%v utility release count=%d memory=%v", err, released, memory)
	}
	if used := memory.InUse(); used.Working != 0 || used.State != 0 {
		t.Fatalf("setup retained plan or frame memory: %+v", used)
	}
}
