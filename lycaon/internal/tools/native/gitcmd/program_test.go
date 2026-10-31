package gitcmd_test

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/lycaon/lycaon/internal/tooloutput"
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
	"github.com/lycaon/lycaon/internal/tools/native"
	"github.com/lycaon/lycaon/internal/tools/native/command"
	"github.com/lycaon/lycaon/internal/tools/native/gitcmd"
	"github.com/lycaon/lycaon/internal/tools/native/mutation"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

func TestProgramEntryRunsGitReadFormsThroughTheUmbrella(t *testing.T) {
	t.Parallel()
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
	natives := map[string]command.NativeOperation{"git": {Prepare: func(_ context.Context, frame *projectpaths.ProgramPaths, dispatch commandprogram.Dispatch, request commandprogram.OperationRequest) (command.NativePrepared, error) {
		prepared, err := owners.PrepareGit(frame, nil, nestedCommand(dispatch), request)
		if err != nil {
			return command.NativePrepared{}, err
		}
		return command.NativePrepared{Command: prepared.Command, Review: prepared.Review, Invoke: prepared.InvokeOwner}, nil
	}}}
	f.runtime.Owners = func(tools.ToolContext, *projectpaths.ProgramPaths, *toolcontract.ProgramMemory, tooloutput.SpillAdmission) (command.ProgramOwners, error) {
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
	t.Parallel()
	f := newProgramFixture(t)
	testutil.FailErr(t, "seed", os.WriteFile(filepath.Join(f.dir, "a"), []byte("a\n"), 0o644))
	owners, err := gitcmd.NewOwners(git.NewManager())
	testutil.FailErr(t, "git owners", err)
	f.runtime.Owners = func(tools.ToolContext, *projectpaths.ProgramPaths, *toolcontract.ProgramMemory, tooloutput.SpillAdmission) (command.ProgramOwners, error) {
		return command.ProgramOwners{Natives: map[string]command.NativeOperation{"git": {Prepare: func(_ context.Context, frame *projectpaths.ProgramPaths, dispatch commandprogram.Dispatch, request commandprogram.OperationRequest) (command.NativePrepared, error) {
			prepared, err := owners.PrepareGit(frame, nil, nestedCommand(dispatch), request)
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

func TestProgramEntryRunsGlobalConfigOverridesNatively(t *testing.T) {
	t.Parallel()
	f := newProgramFixture(t)
	testutil.FailErr(t, "seed", os.WriteFile(filepath.Join(f.dir, "README.txt"), []byte("new\n"), 0o644))
	testutil.FailErr(t, "fresh", os.Mkdir(filepath.Join(f.dir, "fresh"), 0o755))
	owners, err := gitcmd.NewOwners(git.NewManager())
	testutil.FailErr(t, "git owners", err)
	f.runtime.Owners = func(tools.ToolContext, *projectpaths.ProgramPaths, *toolcontract.ProgramMemory, tooloutput.SpillAdmission) (command.ProgramOwners, error) {
		return command.ProgramOwners{Natives: map[string]command.NativeOperation{"git": {Prepare: func(_ context.Context, frame *projectpaths.ProgramPaths, dispatch commandprogram.Dispatch, request commandprogram.OperationRequest) (command.NativePrepared, error) {
			prepared, err := owners.PrepareGit(frame, nil, nestedCommand(dispatch), request)
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
	tc.Host.SessionOutputDir = t.TempDir()
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

func TestProgramEntryAppliesHeredocPatchesThroughBothCommands(t *testing.T) {
	t.Parallel()
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
	testutil.FailErr(t, "seed", os.WriteFile(filepath.Join(f.dir, "input"), []byte("alpha\nbeta\n"), 0o644))
	gitRun("add", ".")
	gitRun("commit", "-q", "-m", "seed commit")

	f.bindMutationOwners(t)
	source := "patch -p1 <<'EOF'\n--- a/input\n+++ b/input\n@@ -1,2 +1,2 @@\n-alpha\n+ALPHA\n beta\nEOF\ngit add input\n" +
		"git apply --index <<'EOF'\ndiff --git a/input b/input\n--- a/input\n+++ b/input\n@@ -1,2 +1,3 @@\n ALPHA\n beta\n+gamma\nEOF\n" +
		"git apply --stat <<'EOF'\n--- a/input\n+++ b/input\n@@ -1 +1 @@\n-ALPHA\n+alpha\nEOF"
	out, err := f.call(t, t.Context(), source)
	testutil.FailErr(t, "run patch program", err)
	var outcome struct {
		Stdout string `json:"stdout"`
		State  string `json:"state"`
	}
	testutil.FailErr(t, "decode outcome", json.Unmarshal([]byte(out), &outcome))
	if outcome.Stdout != "patching file input\n input |    2 +-\n 1 file changed, 1 insertion(+), 1 deletion(-)\n" || outcome.State != "completed" {
		t.Fatalf("program outcome %s", out)
	}
	data, err := os.ReadFile(filepath.Join(f.dir, "input"))
	testutil.FailErr(t, "read input", err)
	if string(data) != "ALPHA\nbeta\ngamma\n" {
		t.Fatalf("input = %q", data)
	}
	staged, err := exec.Command(bin, "-C", f.dir, "show", ":input").Output()
	testutil.FailErr(t, "read index", err)
	if string(staged) != "ALPHA\nbeta\ngamma\n" {
		t.Fatalf("index input = %q", staged)
	}
	if executions := f.executions(t); executions["patch"] != "completed;" || executions["git add"] != "completed;" || executions["git apply"] != "completed;completed;" {
		t.Fatalf("child receipts %v", executions)
	}
}

// bindMutationOwners serves git and patch from one program's mutation owners.
func (f *programFixture) bindMutationOwners(t *testing.T) {
	t.Helper()
	classes, err := toolcontract.NewCommandEffects(toolcontract.CommandReadOnly, toolcontract.CommandMutation)
	testutil.FailErr(t, "compile profile effects", err)
	allowed := map[string]bool{"read": true, "write": true, "delete": true, "chmod": true, "stat": true, "list_dir": true}
	boundary := sandbox.NewBoundary(sandbox.Config{ProjectRootRequired: true, RejectSymlinkEscape: true}, []sandbox.ToolProfile{{ID: "fixture", Tools: allowed, CommandEffects: classes}})
	mutations, err := mutation.NewOwners(native.ExecutionRevisions{Boundary: boundary}, f.recorder, mutation.Operations{Write: &native.WriteTool{Boundary: boundary}, Delete: &native.DeleteTool{Boundary: boundary},
		Move: &native.MoveTool{Boundary: boundary}, Copy: &native.CopyTool{Boundary: boundary}, Mkdir: &native.MkdirTool{Boundary: boundary}, Chmod: &native.ChmodTool{Boundary: boundary},
		Chown: &native.ChownTool{Boundary: boundary}, Times: &native.TimesTool{Boundary: boundary}, Extract: &native.ExtractArchiveTool{Boundary: boundary}})
	testutil.FailErr(t, "mutation owners", err)
	owners, err := gitcmd.NewOwners(git.NewManager())
	testutil.FailErr(t, "git owners", err)
	f.runtime.Owners = func(_ tools.ToolContext, _ *projectpaths.ProgramPaths, memory *toolcontract.ProgramMemory, _ tooloutput.SpillAdmission) (command.ProgramOwners, error) {
		program := *mutations
		program.Revisions.Memory = memory
		return command.ProgramOwners{Natives: map[string]command.NativeOperation{
			"git": {Prepare: func(_ context.Context, frame *projectpaths.ProgramPaths, dispatch commandprogram.Dispatch, request commandprogram.OperationRequest) (command.NativePrepared, error) {
				prepared, err := owners.PrepareGit(frame, &program, nestedCommand(dispatch), request)
				if err != nil {
					return command.NativePrepared{}, err
				}
				return command.NativePrepared{Command: prepared.Command, Mode: prepared.Mode, Review: prepared.Review, Invoke: prepared.InvokeOwner}, nil
			}},
			"patch": {Prepare: func(_ context.Context, frame *projectpaths.ProgramPaths, dispatch commandprogram.Dispatch, request commandprogram.OperationRequest) (command.NativePrepared, error) {
				prepared, err := program.Prepare(mutation.Program{Paths: frame}, request)
				if err != nil {
					return command.NativePrepared{}, err
				}
				return command.NativePrepared{Mode: prepared.Mode, Review: prepared.Review, Invoke: prepared.InvokeOwner}, nil
			}},
		}}, nil
	}
}

func TestGitGoldenProgramsRunThroughTheCommandEntry(t *testing.T) {
	t.Parallel()
	catalog, err := toolcontract.LanguageV1()
	testutil.FailErr(t, "catalog", err)
	var contract struct {
		Golden []struct {
			ID       string                                     `json:"id"`
			Program  string                                     `json:"program"`
			Setup    struct{ Files map[string]json.RawMessage } `json:"setup"`
			Expected struct {
				Stdout string `json:"stdout"`
				Status int    `json:"status"`
			} `json:"expected"`
		} `json:"golden_programs"`
	}
	testutil.FailErr(t, "golden", json.Unmarshal(catalog.Contracts["golden"], &contract))
	ran := 0
	for _, golden := range contract.Golden {
		if !strings.HasPrefix(golden.ID, "git-") {
			continue
		}
		ran++
		t.Run(golden.ID, func(t *testing.T) {
			t.Parallel()
			f := newProgramFixture(t)
			for name, raw := range golden.Setup.Files {
				var content string
				testutil.FailErr(t, "golden file "+name, json.Unmarshal(raw, &content))
				testutil.FailErr(t, "seed "+name, os.WriteFile(filepath.Join(f.dir, name), []byte(content), 0o644))
			}
			f.bindMutationOwners(t)
			out, err := f.call(t, t.Context(), golden.Program)
			testutil.FailErr(t, "run golden program", err)
			var outcome struct {
				Stdout string `json:"stdout"`
				State  string `json:"state"`
				Status int    `json:"status"`
			}
			testutil.FailErr(t, "decode outcome", json.Unmarshal([]byte(out), &outcome))
			if outcome.Stdout != golden.Expected.Stdout || outcome.Status != golden.Expected.Status || outcome.State != "completed" {
				t.Fatalf("program outcome %s", out)
			}
		})
	}
	if ran == 0 {
		t.Fatal("no Git golden programs")
	}
}

func nestedCommand(dispatch commandprogram.Dispatch) commandprogram.NestedCommand {
	nested, _ := dispatch.(commandprogram.NestedCommand)
	return nested
}

func TestProgramEntryBisectRunDispatchesItsCheck(t *testing.T) {
	t.Parallel()
	bin, err := gitengine.BinaryPath()
	testutil.FailErr(t, "bundled git", err)
	f := newProgramFixture(t)
	gitRun := func(args ...string) string {
		t.Helper()
		cmd := exec.Command(bin, args...)
		cmd.Dir = f.dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
			"GIT_AUTHOR_NAME=Ann", "GIT_AUTHOR_EMAIL=ann@example.test", "GIT_COMMITTER_NAME=Ann", "GIT_COMMITTER_EMAIL=ann@example.test")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	gitRun("init", "-q", "-b", "main")
	for i := 1; i <= 12; i++ {
		state := "good\n"
		if i >= 7 {
			state = "bad\n"
		}
		testutil.FailErr(t, "seed state", os.WriteFile(filepath.Join(f.dir, "state"), []byte(state), 0o644))
		gitRun("add", "state")
		gitRun("commit", "-q", "--allow-empty", "-m", fmt.Sprintf("commit %d", i))
	}
	culprit := gitRun("rev-parse", "HEAD~5")
	f.bindMutationOwners(t)
	out, err := f.call(t, t.Context(), "git bisect start HEAD HEAD~11 && git bisect run git grep -q good -- state")
	testutil.FailErr(t, "bisect program", err)
	var outcome struct {
		Stdout string `json:"stdout"`
		State  string `json:"state"`
	}
	testutil.FailErr(t, "decode outcome", json.Unmarshal([]byte(out), &outcome))
	if outcome.State != "completed" || !strings.Contains(outcome.Stdout, culprit+" is the first bad commit\n") || !strings.Contains(outcome.Stdout, "running 'git' 'grep' '-q' 'good' '--' 'state'\n") {
		t.Fatalf("program outcome %s", out)
	}
	if executions := f.executions(t); executions["git bisect start"] != "completed;" || executions["git bisect run"] != "completed;" || !strings.HasPrefix(executions["git grep"], "completed;") {
		t.Fatalf("child receipts %v", executions)
	}
	if _, err := os.Stat(filepath.Join(f.dir, ".git", "BISECT_START")); err != nil {
		t.Fatal("bisect session ended before git bisect reset")
	}
}
