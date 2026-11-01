//go:build integration

package sessions

import (
	"context"
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
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

func credentialCommandOutput(t *testing.T, files *credentialFiles, root string) (string, error) {
	t.Helper()
	f := programtest.New(t, programtest.Options{})
	f.Dir = root
	f.Configure = func(tc *tools.ToolContext) { tc.Effects.CredentialFiles = files }
	owners, err := literal.NewOwners(native.ExecutionRevisions{Boundary: f.Runtime.Boundary}, f.Recorder, literal.DomainOwners{})
	testutil.FailErr(t, "bind credential reader", err)
	f.Runtime.Owners = func(_ tools.ToolContext, _ *projectpaths.ProgramPaths, memory *toolcontract.ProgramMemory, _ tooloutput.SpillAdmission) (command.ProgramOwners, error) {
		bound := owners.ForProgram(memory)
		return command.ProgramOwners{Natives: map[string]command.NativeOperation{"cat": {Tool: "read", Prepare: func(_ context.Context, paths *projectpaths.ProgramPaths, _ commandprogram.Dispatch, request commandprogram.OperationRequest) (command.NativePrepared, error) {
			prepared, err := bound.Prepare(paths, request)
			return command.NativePrepared{Tool: prepared.Operation, Review: prepared.Review, Invoke: prepared.InvokeOwner}, err
		}}}}, nil
	}
	return f.Call(t, t.Context(), "cat .env")
}
