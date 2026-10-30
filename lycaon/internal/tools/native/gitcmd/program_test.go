package gitcmd_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/blobstore"
	"github.com/lycaon/lycaon/internal/captureprojection"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolexecution"
	"github.com/lycaon/lycaon/internal/toolprofiles"
	"github.com/lycaon/lycaon/pkg/api"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/gitengine"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/command"
	"github.com/lycaon/lycaon/internal/tools/native/gitcmd"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

// TestProgramEntryRunsGitReadFormsThroughTheUmbrella: git subcommands,
// including ones behind global -C, reach the Git owner from a program, run
// as their catalog command's execution, and compose with native stages.
func TestProgramEntryRunsGitReadFormsThroughTheUmbrella(t *testing.T) {
	bin, err := gitengine.BinaryPath()
	testutil.FailErr(t, "bundled git", err)
	f := newProgramFixture(t)
	gitRun := func(args ...string) {
		t.Helper()
		cmd := exec.Command(bin, args...)
		cmd.Dir = f.dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
			"GIT_AUTHOR_NAME=Ann", "GIT_AUTHOR_EMAIL=ann@example.test", "GIT_COMMITTER_NAME=Ann", "GIT_COMMITTER_EMAIL=ann@example.test")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	gitRun("init", "-q", "-b", "main")
	testutil.FailErr(t, "mkdir", os.MkdirAll(filepath.Join(f.dir, "sub"), 0o755))
	testutil.FailErr(t, "seed", os.WriteFile(filepath.Join(f.dir, "sub", "a.txt"), []byte("a\n"), 0o644))
	gitRun("add", ".")
	gitRun("commit", "-q", "-m", "seed commit")
	testutil.FailErr(t, "dirty", os.WriteFile(filepath.Join(f.dir, "new.txt"), []byte("n\n"), 0o644))

	owners, err := gitcmd.NewOwners(git.NewManager())
	testutil.FailErr(t, "git owners", err)
	natives := map[string]command.NativeOperation{"git": {Prepare: func(_ context.Context, frame *projectpaths.ProgramPaths, _ commandprogram.Dispatch, request commandprogram.OperationRequest) (command.NativePrepared, error) {
		prepared, err := owners.PrepareGit(frame, request)
		if err != nil {
			return command.NativePrepared{}, err
		}
		return command.NativePrepared{Command: prepared.Command, Review: prepared.Review, Invoke: prepared.InvokeOwner}, nil
	}}}
	f.runtime.Owners = func(tools.ToolContext, *projectpaths.ProgramPaths) (command.ProgramOwners, error) {
		return command.ProgramOwners{Natives: natives}, nil
	}
	out, err := f.call(t, t.Context(), "git status -sb; git -C sub ls-files; git --no-pager log --format=%s")
	testutil.FailErr(t, "run git program", err)
	var outcome struct {
		Stdout string `json:"stdout"`
		State  string `json:"state"`
	}
	testutil.FailErr(t, "decode outcome", json.Unmarshal([]byte(out), &outcome))
	if outcome.Stdout != "## main\n?? new.txt\na.txt\nseed commit\n" || outcome.State != "completed" {
		t.Fatalf("program outcome %s", out)
	}
	if executions := f.executions(t); executions["git status"] != "completed;" || executions["git ls-files"] != "completed;" || executions["git log"] != "completed;" {
		t.Fatalf("child receipts %v", executions)
	}

	customPath, err := f.call(t, t.Context(), "PATH=/custom/bin git status -sb")
	if !strings.Contains(customPath+fmt.Sprint(err), "COMMAND_OPERAND_INVALID") {
		t.Fatalf("custom PATH ran: %s %v", customPath, err)
	}
	reservedEnv, err := f.call(t, t.Context(), "LD_PRELOAD=/tmp/inject.so git status -sb")
	if !strings.Contains(reservedEnv+fmt.Sprint(err), "COMMAND_OPERAND_INVALID") {
		t.Fatalf("host-reserved env ran: %s %v", reservedEnv, err)
	}

	t.Setenv("PATH", "")
	refused, err := f.call(t, t.Context(), "git branch created")
	if !strings.Contains(refused+fmt.Sprint(err), "COMMAND_OPERAND_INVALID") {
		t.Fatalf("write spelling ran: %s %v", refused, err)
	}
	unsupported, err := f.call(t, t.Context(), "git gc")
	if !strings.Contains(unsupported+fmt.Sprint(err), "COMMAND_NATIVE_FORM_UNSUPPORTED") {
		t.Fatalf("uncatalogued git ran: %s %v", unsupported, err)
	}
}

func TestProgramEntryInitializesRepositoryAndCommits(t *testing.T) {
	f := newProgramFixture(t)
	testutil.FailErr(t, "seed", os.WriteFile(filepath.Join(f.dir, "a"), []byte("a\n"), 0o644))
	owners, err := gitcmd.NewOwners(git.NewManager())
	testutil.FailErr(t, "git owners", err)
	f.runtime.Owners = func(tools.ToolContext, *projectpaths.ProgramPaths) (command.ProgramOwners, error) {
		return command.ProgramOwners{Natives: map[string]command.NativeOperation{"git": {Prepare: func(_ context.Context, frame *projectpaths.ProgramPaths, _ commandprogram.Dispatch, request commandprogram.OperationRequest) (command.NativePrepared, error) {
			prepared, err := owners.PrepareGit(frame, request)
			if err != nil {
				return command.NativePrepared{}, err
			}
			return command.NativePrepared{Command: prepared.Command, Review: prepared.Review, Invoke: prepared.InvokeOwner}, nil
		}}}}, nil
	}
	out, err := f.call(t, t.Context(), "git init -q && git add a && git commit -q -m first && git status --short")
	testutil.FailErr(t, "first commit workflow", err)
	var outcome struct {
		Stdout string `json:"stdout"`
		State  string `json:"state"`
	}
	testutil.FailErr(t, "decode outcome", json.Unmarshal([]byte(out), &outcome))
	if outcome.Stdout != "" || outcome.State != "completed" {
		t.Fatalf("program outcome %s", out)
	}
	if executions := f.executions(t); executions["git init"] != "completed;" || executions["git add"] != "completed;" || executions["git commit"] != "completed;" || executions["git status"] != "completed;" {
		t.Fatalf("child receipts %v", executions)
	}
	if _, err := os.Stat(filepath.Join(f.dir, ".git", "HEAD")); err != nil {
		testutil.FailErr(t, "stat repository HEAD", err)
	}
}

// Global options before the subcommand, including identity and initial
// branch overrides, run natively.
func TestProgramEntryRunsGlobalConfigOverridesNatively(t *testing.T) {
	f := newProgramFixture(t)
	testutil.FailErr(t, "seed", os.WriteFile(filepath.Join(f.dir, "README.txt"), []byte("new\n"), 0o644))
	testutil.FailErr(t, "fresh", os.Mkdir(filepath.Join(f.dir, "fresh"), 0o755))
	owners, err := gitcmd.NewOwners(git.NewManager())
	testutil.FailErr(t, "git owners", err)
	f.runtime.Owners = func(tools.ToolContext, *projectpaths.ProgramPaths) (command.ProgramOwners, error) {
		return command.ProgramOwners{Natives: map[string]command.NativeOperation{"git": {Prepare: func(_ context.Context, frame *projectpaths.ProgramPaths, _ commandprogram.Dispatch, request commandprogram.OperationRequest) (command.NativePrepared, error) {
			prepared, err := owners.PrepareGit(frame, request)
			if err != nil {
				return command.NativePrepared{}, err
			}
			return command.NativePrepared{Command: prepared.Command, Review: prepared.Review, Invoke: prepared.InvokeOwner}, nil
		}}}}, nil
	}
	out, err := f.call(t, t.Context(), "git --no-pager -C fresh -c init.defaultBranch=other init -q -b trunk && git -c init.defaultBranch=other init -q && git add README.txt && "+
		"git -c user.name=Test -c user.email=test@example.invalid commit -qm first && git log -1 --format='%an <%ae> %s'")
	testutil.FailErr(t, "global options workflow", err)
	var outcome struct {
		Stdout string `json:"stdout"`
		State  string `json:"state"`
	}
	testutil.FailErr(t, "decode outcome", json.Unmarshal([]byte(out), &outcome))
	if outcome.Stdout != "Test <test@example.invalid> first\n" || outcome.State != "completed" {
		t.Fatalf("program outcome %s", out)
	}
	if executions := f.executions(t); executions["git init"] != "completed;completed;" || executions["git commit"] != "completed;" {
		t.Fatalf("child receipts %v", executions)
	}
	for dir, want := range map[string]string{"fresh": "ref: refs/heads/trunk\n", ".": "ref: refs/heads/other\n"} {
		head, err := os.ReadFile(filepath.Join(f.dir, dir, ".git", "HEAD"))
		testutil.FailErr(t, "read HEAD", err)
		if string(head) != want {
			t.Fatalf("%s HEAD = %q, want %q", dir, head, want)
		}
	}
}

type fixtureSessions struct{}

func (fixtureSessions) Get(_ context.Context, id string) (*api.Session, error) {
	return &api.Session{ID: id, OwnerPersonID: "fixture-person"}, nil
}

type progressFacts struct{ store progress.Store }

func (p progressFacts) ProgramProgressState(ctx context.Context, id string) (progress.PrerequisiteState, error) {
	return progress.PrerequisiteState{SessionID: id, RootSessionID: id, Content: p.store.Get(ctx, id)}, nil
}

type programFixture struct {
	runtime  *command.ProgramRuntime
	recorder *invocation.SQLRecorder
	database db.Handle
	session  string
	dir      string
	calls    int
}

// newProgramFixture composes the real registry, executor, SQL receipts and
// supervisor around the command entry, as command's own entry tests do.
func newProgramFixture(t *testing.T) *programFixture {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	testutil.FailErr(t, "resolve dir", err)
	database := testdbfixture.Open(t, "programs.db")
	session := "session-1"
	testdbseed.InsertSession(t, database, session, testdbseed.DefaultProjectID)
	recorder := invocation.NewSQLRecorder(database)
	registry := tools.NewDefaultRegistry()
	classes, err := toolcontract.NewCommandEffects(toolcontract.CommandReadOnly, toolcontract.CommandMutation)
	testutil.FailErr(t, "compile profile effects", err)
	boundary := sandbox.NewBoundary(sandbox.Config{ProjectRootRequired: true, RejectSymlinkEscape: true}, []sandbox.ToolProfile{{ID: "fixture", CommandEffects: classes}})
	executor := toolexecution.NewExecutor(toolprofiles.NewProfilePolicyEngine(boundary), registry, "fixture")
	owner := progress.NewMemoryStore()
	testutil.FailErr(t, "set actual progress", owner.Set(session, "- [ ] Run the fixture program"))
	background := bgprocess.NewRegistry(bgprocess.DefaultConfig(), bgprocess.Hooks{})
	t.Cleanup(func() { _ = background.Lifecycle.Close(context.Background()) })
	runtime := &command.ProgramRuntime{
		Executor: executor, Registry: registry, Recorder: recorder, Blobs: blobstore.Store{Root: t.TempDir()},
		Projector: captureprojection.New(secretmatch.NewInertMatcher(), nil), Boundary: boundary,
		Progress: progressFacts{owner}, Supervisor: &command.ProgramSupervisor{Interrupter: recorder, Background: background}, Evaluate: commandprogram.Evaluate,
		Sessions: fixtureSessions{},
	}
	return &programFixture{runtime: runtime, recorder: recorder, database: database, session: session, dir: dir}
}

func (f *programFixture) call(t *testing.T, ctx context.Context, source string) (string, error) {
	t.Helper()
	f.calls++
	callID := fmt.Sprintf("model-call-%d", f.calls)
	args := map[string]any{"command": source}
	contract, _ := toolcontract.Lookup("command")
	parent, err := f.recorder.Begin(t.Context(), invocation.Start{ProjectID: testdbseed.DefaultProjectID, SessionID: f.session, ToolCallID: callID, ToolName: "command", Args: args, Contract: contract})
	testutil.FailErr(t, "begin model receipt", err)
	tc := tools.ToolContext{Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "r1", Label: "root", Path: f.dir, IsPrimary: true}}, ActiveRootID: "r1", SourceWorkspaceKind: api.SourceWorkspaceKindProject}}
	tc.Identity.Agent, tc.Identity.ProjectID, tc.Identity.SessionID, tc.Identity.ToolCallID = "fixture", testdbseed.DefaultProjectID, f.session, callID
	tc.Invocation.ID = parent.ID
	return f.runtime.Run(ctx, args, tc)
}

func (f *programFixture) executions(t *testing.T) map[string]string {
	t.Helper()
	rows, err := f.database.QueryContext(t.Context(), `SELECT operation_id, state FROM invocation_program_executions ORDER BY ordinal`)
	testutil.FailErr(t, "read executions", err)
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var operation, state string
		testutil.FailErr(t, "scan execution", rows.Scan(&operation, &state))
		out[operation] += state + ";"
	}
	return out
}
