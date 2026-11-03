package contract

import (
	"testing"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/programtest"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolexecution"
	"github.com/lycaon/lycaon/internal/tooloutput"
	"github.com/lycaon/lycaon/internal/toolprofiles"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
	"github.com/lycaon/lycaon/internal/tools/native/command"
	"github.com/lycaon/lycaon/internal/tools/native/mutation"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

func verificationProgramFixture(t *testing.T) (*programtest.Fixture, *recordingGate) {
	t.Helper()
	f := programtest.New(t, programtest.Options{})
	gate := &recordingGate{}
	executor := toolexecution.NewExecutor(toolexecution.NewApprovalPolicyEngine(toolprofiles.NewProfilePolicyEngine(f.Boundary), gate), f.Runtime.Registry, "fixture")
	executor.Approvals.SetCheckpointManager(t.Context(), nil, gate)
	f.Runtime.Executor = executor
	f.Runtime.Owners = func(tc tools.ToolContext, paths *projectpaths.ProgramPaths, memory *toolcontract.ProgramMemory, admission tooloutput.SpillAdmission) (command.ProgramOwners, error) {
		b := f.Boundary
		owners, err := mutation.NewOwners(native.ExecutionRevisions{Boundary: b, Memory: memory}, f.Recorder, mutation.Operations{
			Write: &native.WriteTool{Boundary: b}, Delete: &native.DeleteTool{Boundary: b}, Move: &native.MoveTool{Boundary: b}, Copy: &native.CopyTool{Boundary: b}, Mkdir: &native.MkdirTool{Boundary: b}, Chmod: &native.ChmodTool{Boundary: b}, Chown: &native.ChownTool{Boundary: b}, Times: &native.TimesTool{Boundary: b}, Extract: &native.ExtractArchiveTool{Boundary: b},
		})
		if err != nil {
			return command.ProgramOwners{}, err
		}
		owners.Admission = admission
		return command.ProgramOwners{Redirections: func(frame *projectpaths.ProgramPaths, review tools.CommandOutputAdmission) (commandprogram.InputRedirection, commandprogram.OutputPublication) {
			p := &mutation.Publication{Owners: owners, Program: mutation.Program{Paths: frame}, Tools: tc, Admission: review}
			return p, p
		}}, nil
	}
	return f, gate
}
