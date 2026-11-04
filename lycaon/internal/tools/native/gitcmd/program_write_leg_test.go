package gitcmd_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/programtest"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/command"
	"github.com/lycaon/lycaon/internal/tools/native/gitcmd"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
	"github.com/lycaon/lycaon/pkg/api"
)

type programBranch struct{}

func (programBranch) ValidateMeta(context.Context) error { return nil }

func (programBranch) EnsureParents(context.Context, string) error { return nil }

func newGitProgramFixture(t *testing.T, workerBranch bool) *programtest.Fixture {
	t.Helper()
	owners, err := gitcmd.NewOwners(git.NewManager())
	testutil.FailErr(t, "git owners", err)
	f := programtest.New(t, programtest.Options{Natives: func(string) map[string]command.NativeOperation {
		return map[string]command.NativeOperation{"git": {Prepare: func(_ context.Context, frame *projectpaths.ProgramPaths, dispatch commandprogram.Dispatch, request commandprogram.OperationRequest) (command.NativePrepared, error) {
			prepared, err := owners.PrepareGit(frame, nil, nestedCommand(dispatch), request)
			if err != nil {
				return command.NativePrepared{}, err
			}
			return command.NativePrepared{Command: prepared.Command, Review: prepared.Review, Invoke: prepared.InvokeOwner}, nil
		}}}
	}})
	if workerBranch {
		f.Configure = func(tc *tools.ToolContext) {
			tc.Source.SourceWorkspaceKind, tc.Identity.WorkerJobID = api.SourceWorkspaceKindWorker, "job-1"
			tc.Source.WorkerBranchRoot, tc.Source.BranchWorkspace = f.Dir, programBranch{}
		}
	}
	return f
}

func runGitProgram(t *testing.T, f *programtest.Fixture, source string) command.ProgramOutcome {
	t.Helper()
	out, err := f.Call(t, t.Context(), source)
	testutil.FailErr(t, "run program", err)
	var outcome command.ProgramOutcome
	testutil.FailErr(t, "decode outcome", json.Unmarshal([]byte(out), &outcome))
	return outcome
}

func TestACOP01AndACOP02WriteLegProgramsStopAtTheRefusal(t *testing.T) {
	for _, source := range []string{"git status", "git diff -- src/main.go", "git log", "git status || true"} {
		t.Run(source, func(t *testing.T) {
			outcome := runGitProgram(t, newGitProgramFixture(t, true), "printf before; "+source+"; printf after")
			if outcome.Refusal == nil || outcome.Refusal.Code != "GIT_NO_REPOSITORY_ON_WRITE_LEG" || outcome.StopCode != "GIT_NO_REPOSITORY_ON_WRITE_LEG" {
				t.Fatalf("outcome = %+v, want the refusal as the stop", outcome)
			}
			if outcome.Stdout != "before" {
				t.Fatalf("stdout = %q, want the program stopped at the refusal", outcome.Stdout)
			}
		})
	}
}

func TestACOP03ProgramsWithoutARefusalKeepGitsStatus(t *testing.T) {
	for _, tc := range []struct {
		name         string
		workerBranch bool
		source       string
	}{
		{"native git status on the primary tree", false, "git status"},
		{"external git status on the primary tree", false, "external -- git status"},
		{"external git status on a write leg", true, "external -- git status"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			outcome := runGitProgram(t, newGitProgramFixture(t, tc.workerBranch), tc.source)
			if outcome.Refusal != nil || outcome.Status != 128 {
				t.Fatalf("outcome = %+v, want Git's status 128 and no refusal", outcome)
			}
		})
	}
}
