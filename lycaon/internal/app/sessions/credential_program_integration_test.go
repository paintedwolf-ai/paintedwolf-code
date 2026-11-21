//go:build integration

package sessions

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/programtest"
	"github.com/lycaon/lycaon/internal/repochange"
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

func TestScratchAndWorkspaceCopiesKeepCredentialIndexReady(t *testing.T) {
	ctx := t.Context()
	files := credentialFilesFixture(t)
	root := attachCredentialRoot(t, files)
	capture := &credentialRoot{
		projectID: "proj-1", id: "r1", path: root,
		known: map[string]bool{}, epoch: repochange.CurrentEpoch(root), ready: true,
	}
	files.index.roots["proj-1\x00r1"] = capture
	testutil.FailErr(t, "seed copy source", os.WriteFile(filepath.Join(root, "fixture.txt"), []byte("TOKEN=workspace-fixture-4411\n"), 0o600))
	updates := make(chan error, 4)
	stop := repochange.RegisterObserver(func(ctx context.Context, event repochange.Event) {
		if event.Kind == repochange.WorktreeChanged && event.ProjectDir == root {
			updates <- files.index.changed(ctx, event)
		}
	})
	defer stop()
	f := programtest.New(t, programtest.Options{})
	f.Dir = root
	copyTool := &native.CopyTool{Boundary: f.Runtime.Boundary}
	pairs := []any{
		map[string]any{"from": "fixture.txt", "to": "@scratch/generated.txt"},
		map[string]any{"from": "fixture.txt", "to": ".env"},
	}
	_, err := f.Invoke(t, ctx, "copy", map[string]any{"copies": pairs}, copyTool.Run)
	testutil.FailErr(t, "copy into scratch and the workspace", err)
	repochange.ResetDebouncerForTest(ctx)
	testutil.FailErr(t, "refresh credential index", testutil.Receive(t, "workspace update", updates))
	if !capture.ready || capture.epoch != repochange.CurrentEpoch(root) || !capture.known[".env"] {
		t.Fatalf("credential index lost its workspace revision: %+v", capture)
	}
	values := files.harvest.FileValues("proj-1")
	if len(values) != 1 || values[0].Secret() != "workspace-fixture-4411" {
		t.Fatal("credential index did not capture the workspace file")
	}
}
