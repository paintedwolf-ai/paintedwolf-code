package gitcmd_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/programtest"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/command"
	"github.com/lycaon/lycaon/internal/tools/native/gitcmd"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
	"github.com/lycaon/lycaon/internal/toolsurface"
	"github.com/lycaon/lycaon/pkg/api"
)

type programBranch struct{}

func (programBranch) ValidateMeta(context.Context) error { return nil }

func (programBranch) EnsureParents(context.Context, string) error { return nil }

type gitProgramFixture struct {
	*programtest.Fixture
	out *tools.ToolInvocationOut
}

func newGitProgramFixture(t *testing.T, workerBranch bool) *gitProgramFixture {
	t.Helper()
	owners, err := gitcmd.NewOwners(git.NewManager())
	testutil.FailErr(t, "git owners", err)
	f := &gitProgramFixture{out: &tools.ToolInvocationOut{}}
	f.Fixture = programtest.New(t, programtest.Options{Natives: func(string) map[string]command.NativeOperation {
		return map[string]command.NativeOperation{"git": {Prepare: func(_ context.Context, frame *projectpaths.ProgramPaths, dispatch commandprogram.Dispatch, request commandprogram.OperationRequest) (command.NativePrepared, error) {
			prepared, err := owners.PrepareGit(frame, nil, nestedCommand(dispatch), request)
			if err != nil {
				return command.NativePrepared{}, err
			}
			return command.NativePrepared{Command: prepared.Command, Review: prepared.Review, Invoke: prepared.InvokeOwner}, nil
		}}}
	}})
	f.Configure = func(tc *tools.ToolContext) {
		tc.Effects.Out = f.out
		if workerBranch {
			tc.Source.SourceWorkspaceKind, tc.Identity.WorkerJobID = api.SourceWorkspaceKindWorker, "job-1"
			tc.Source.WorkerBranchRoot, tc.Source.BranchWorkspace = f.Dir, programBranch{}
		}
	}
	return f
}

func (f *gitProgramFixture) warned() bool {
	return f.out.Facts.HasCode(toolrejection.GitNoRepositoryOnWriteLegCode)
}

func runGitProgram(t *testing.T, f *gitProgramFixture, source string) command.ProgramOutcome {
	t.Helper()
	out, err := f.Call(t, t.Context(), source)
	testutil.FailErr(t, "run program", err)
	var outcome command.ProgramOutcome
	testutil.FailErr(t, "decode outcome", json.Unmarshal([]byte(out), &outcome))
	return outcome
}

func TestACOP01WriteLegGitFailsLikeGitAndTheCallIsWarned(t *testing.T) {
	for _, source := range []string{"git status", "git diff -- src/main.go", "git log"} {
		t.Run(source, func(t *testing.T) {
			f := newGitProgramFixture(t, true)
			outcome := runGitProgram(t, f, source)
			if outcome.Refusal != nil || outcome.StopCode != "" || outcome.Status != 128 {
				t.Fatalf("outcome = %+v, want Git's status 128 and no refusal", outcome)
			}
			if !f.warned() {
				t.Fatalf("facts = %+v, want the warning's fact", f.out.Facts)
			}
		})
	}
}

// The call's frozen plan decides, through the program's own tool context.
func TestACOP01WriteLegWarningStatesWhetherThePlanOffersSourceHistory(t *testing.T) {
	for _, tc := range []struct {
		name string
		plan toolsurface.Plan
		want bool
	}{
		{"immediate", toolsurface.Compile([]string{"command", "source_history"}, nil), true},
		{"deferred", toolsurface.Compile([]string{"command"}, []string{"source_history"}), true},
		{"excluded", toolsurface.Compile([]string{"command"}, nil), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newGitProgramFixture(t, true)
			configure := f.Configure
			f.Configure = func(toolCtx *tools.ToolContext) {
				configure(toolCtx)
				toolCtx.Turn.TurnToolPlan = tc.plan
			}
			runGitProgram(t, f, "git status")
			details := f.out.Facts.FeedbackFor(toolrejection.GitNoRepositoryOnWriteLegCode).Details
			if details["source_history_offered"] != tc.want {
				t.Fatalf("details = %+v, want source_history_offered %v", details, tc.want)
			}
		})
	}
}

// A directory outside the attached folders and grants never reaches Git, so the
// warning's reasons are only ever stated about one a read may name.
func TestACOP01WriteLegAbsoluteDirectoryOutsideTheLeg(t *testing.T) {
	t.Run("ungranted is refused without the warning", func(t *testing.T) {
		f := newGitProgramFixture(t, true)
		outcome := runGitProgram(t, f, "git -C '"+t.TempDir()+"' status")
		if outcome.StopCode != "SURVEY_PATH_ESCAPE" || f.warned() {
			t.Fatalf("outcome = %+v warned %v, want the path refusal and no warning", outcome, f.warned())
		}
	})
	t.Run("granted is Git's answer and the warning", func(t *testing.T) {
		f := newGitProgramFixture(t, true)
		dir, err := filepath.EvalSymlinks(t.TempDir())
		testutil.FailErr(t, "canonical directory", err)
		configure := f.Configure
		f.Configure = func(toolCtx *tools.ToolContext) {
			configure(toolCtx)
			toolCtx.Files.ApprovedFileAccess = []hitl.GrantedPathDelta{{Path: dir}}
		}
		outcome := runGitProgram(t, f, "git -C '"+dir+"' status")
		if outcome.Refusal != nil || outcome.Status != 128 || !f.warned() {
			t.Fatalf("outcome = %+v warned %v, want Git's status 128 and the warning", outcome, f.warned())
		}
	})
}

func TestACOP01ManyReadsInOneProgramStateOneWarning(t *testing.T) {
	f := newGitProgramFixture(t, true)
	runGitProgram(t, f, "git status; git log; git diff -- a.go")
	count := 0
	for _, feedback := range f.out.Facts.Feedback {
		if feedback.Code == toolrejection.GitNoRepositoryOnWriteLegCode {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("warnings = %d in %+v, want one", count, f.out.Facts)
	}
}

// Pipeline stages and jobs run concurrently and each has its own effects' view,
// which is why the record is shared.
func TestACOP01ConcurrentReadsStateOneWarning(t *testing.T) {
	for _, source := range []string{"git status | cat", "git status & wait", "git status & git log & wait"} {
		t.Run(source, func(t *testing.T) {
			f := newGitProgramFixture(t, true)
			outcome := runGitProgram(t, f, source)
			if outcome.Refusal != nil || outcome.StopCode != "" {
				t.Fatalf("outcome = %+v, want the program to finish", outcome)
			}
			count := 0
			for _, feedback := range f.out.Facts.Feedback {
				if feedback.Code == toolrejection.GitNoRepositoryOnWriteLegCode {
					count++
				}
			}
			if count != 1 {
				t.Fatalf("warnings = %d in %+v, want one", count, f.out.Facts)
			}
		})
	}
}

func TestACOP02WriteLegProbesRunLikeBash(t *testing.T) {
	for _, source := range []string{"git status || true", "git status; printf after", "if git status; then printf yes; else printf after; fi"} {
		t.Run(source, func(t *testing.T) {
			f := newGitProgramFixture(t, true)
			outcome := runGitProgram(t, f, source)
			if outcome.Refusal != nil || outcome.Status != 0 || !f.warned() {
				t.Fatalf("outcome = %+v warned %v, want the program to continue", outcome, f.warned())
			}
			if source != "git status || true" && outcome.Stdout != "after" {
				t.Fatalf("stdout = %q, want the following statement to run", outcome.Stdout)
			}
		})
	}
}

func TestACOP03NoWarningOffAWorkerBranchOrFromExternalGit(t *testing.T) {
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
			f := newGitProgramFixture(t, tc.workerBranch)
			outcome := runGitProgram(t, f, tc.source)
			if outcome.Refusal != nil || outcome.Status != 128 || f.warned() {
				t.Fatalf("outcome = %+v warned %v, want Git's status 128 and no warning", outcome, f.warned())
			}
		})
	}
}

type parkedGitProgram struct {
	parked chan tools.ProgramPark
}

func (p *parkedGitProgram) ParkProgram(_ context.Context, _ tools.ToolContext, park tools.ProgramPark) (tools.ParkedProgram, error) {
	if !park.Claim() {
		return nil, tools.ErrWaitEnded
	}
	p.parked <- park
	return parkedGitLease{}, nil
}

type parkedGitLease struct{}

func (parkedGitLease) LeaseID() string { return "lease" }

func (parkedGitLease) Settle(context.Context, string, error) {}

// A parked call's result is settled at the park, so what ran before it is
// stated there and what runs after it never reaches the call.
func TestACOP01ParkedProgramWarnsForReadsBeforeThePark(t *testing.T) {
	for _, tc := range []struct {
		source string
		want   bool
	}{
		{"git status; sleep 60", true},
		{"sleep 60; git status", false},
	} {
		t.Run(tc.source, func(t *testing.T) {
			f := newGitProgramFixture(t, true)
			parking := &parkedGitProgram{parked: make(chan tools.ProgramPark, 1)}
			f.Runtime.Parking, f.Runtime.ParkThreshold = parking, time.Nanosecond
			raw, err := f.Call(t, t.Context(), tc.source)
			testutil.FailErr(t, "park program", err)
			if !strings.Contains(raw, `"state":"parked"`) {
				t.Fatalf("parked result %s", raw)
			}
			if f.warned() != tc.want {
				t.Fatalf("warned = %v, want %v", f.warned(), tc.want)
			}
			park := testutil.Receive(t, "the program parks", parking.parked)
			park.Interrupt(t.Context())
		})
	}
}
