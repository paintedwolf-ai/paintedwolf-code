package command_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/programtest"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tooloutput"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
	"github.com/lycaon/lycaon/internal/tools/native/command"
	"github.com/lycaon/lycaon/internal/tools/native/mutation"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

func mutationProgramFixture(t *testing.T) *programtest.Fixture {
	t.Helper()
	return programtest.New(t, programtest.Options{Owners: func(ports programtest.OwnerPorts) func(tools.ToolContext, *projectpaths.ProgramPaths, *toolcontract.ProgramMemory, tooloutput.SpillAdmission) (command.ProgramOwners, error) {
		boundary := ports.Boundary
		mutations, err := mutation.NewOwners(native.ExecutionRevisions{Boundary: boundary}, ports.Recorder, mutation.Operations{
			Write: &native.WriteTool{Boundary: boundary}, Delete: &native.DeleteTool{Boundary: boundary},
			Move: &native.MoveTool{Boundary: boundary}, Copy: &native.CopyTool{Boundary: boundary},
			Mkdir: &native.MkdirTool{Boundary: boundary}, Chmod: &native.ChmodTool{Boundary: boundary},
			Chown: &native.ChownTool{Boundary: boundary}, Times: &native.TimesTool{Boundary: boundary},
			Extract: &native.ExtractArchiveTool{Boundary: boundary},
		})
		testutil.FailErr(t, "mutation operations", err)
		return func(tc tools.ToolContext, paths *projectpaths.ProgramPaths, _ *toolcontract.ProgramMemory, _ tooloutput.SpillAdmission) (command.ProgramOwners, error) {
			natives := map[string]command.NativeOperation{}
			for _, name := range []string{"touch", "mv"} {
				tool, _ := mutations.Operation(name)
				natives[name] = command.NativeOperation{Tool: tool, Prepare: func(_ context.Context, frame *projectpaths.ProgramPaths, _ commandprogram.Dispatch, request commandprogram.OperationRequest) (command.NativePrepared, error) {
					prepared, err := mutations.Prepare(mutation.Program{Paths: frame}, request)
					if err != nil {
						return command.NativePrepared{}, err
					}
					return command.NativePrepared{Mode: prepared.Mode, Review: prepared.Review, Invoke: prepared.InvokeOwner}, nil
				}}
			}
			return command.ProgramOwners{Natives: natives,
				Publication: func(admit tools.CommandOutputAdmission) commandprogram.OutputPublication {
					return &mutation.Publication{Owners: mutations, Program: mutation.Program{Paths: paths}, Tools: tc, Admission: admit}
				},
				Redirections: func(frame *projectpaths.ProgramPaths, admit tools.CommandOutputAdmission) (commandprogram.InputRedirection, commandprogram.OutputPublication) {
					publication := &mutation.Publication{Owners: mutations, Program: mutation.Program{Paths: frame}, Tools: tc, Admission: admit}
					return publication, publication
				},
			}, nil
		}
	}})
}

func TestProgramProjectProgressAndScratchMove(t *testing.T) {
	f := mutationProgramFixture(t)
	testutil.FailErr(t, "clear checklist", f.SetProgress(""))
	for _, source := range []string{"touch config.json", "true > config.json"} {
		raw, err := f.Call(t, t.Context(), source)
		testutil.FailErr(t, "run refused project write", err)
		outcome := decodeOutcome(t, raw)
		if outcome.Refusal == nil || outcome.Refusal.Code != "PROGRESS_MISSING" || !strings.Contains(outcome.Stderr, "[PROGRESS_MISSING]") {
			t.Fatalf("%q: %+v", source, outcome)
		}
		if _, err := os.Stat(filepath.Join(f.Dir, "config.json")); !os.IsNotExist(err) {
			t.Fatalf("%q created project file: %v", source, err)
		}
	}
	raw, err := f.Call(t, t.Context(), `touch "$TMPDIR/temporary.txt"`)
	testutil.FailErr(t, "touch scratch", err)
	if outcome := decodeOutcome(t, raw); outcome.Refusal != nil || outcome.Status != 0 {
		t.Fatalf("scratch write = %+v", outcome)
	}
	scratchFile := filepath.Join(f.ScratchDir, "temporary.txt")
	if _, err := os.Stat(scratchFile); err != nil {
		t.Fatalf("scratch file missing: %v", err)
	}
	raw, err = f.Call(t, t.Context(), `mv "$TMPDIR/temporary.txt" config.json`)
	testutil.FailErr(t, "move scratch without progress", err)
	if outcome := decodeOutcome(t, raw); outcome.Refusal == nil || outcome.Refusal.Code != "PROGRESS_MISSING" {
		t.Fatalf("move admitted without checklist: %+v", outcome)
	}
	if _, err := os.Stat(scratchFile); err != nil {
		t.Fatalf("rejected move removed scratch source: %v", err)
	}
	testutil.FailErr(t, "record checklist", f.SetProgress("- [ ] Publish config.json"))
	raw, err = f.Call(t, t.Context(), `mv "$TMPDIR/temporary.txt" config.json`)
	testutil.FailErr(t, "move scratch with progress", err)
	if outcome := decodeOutcome(t, raw); outcome.Refusal != nil || outcome.Status != 0 {
		t.Fatalf("approved move = %+v", outcome)
	}
	if _, err := os.Stat(filepath.Join(f.Dir, "config.json")); err != nil {
		t.Fatalf("project destination missing: %v", err)
	}
	if _, err := os.Stat(scratchFile); !os.IsNotExist(err) {
		t.Fatalf("move retained scratch source: %v", err)
	}
}
