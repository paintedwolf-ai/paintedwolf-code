package wiring

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/gitengine"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestWorkflowScratchLoopPublishesOnlyFinalResult(t *testing.T) {
	f := newCommandWorkflowFixture(t, []workflowTurn{{
		callID:  "scratch-loop",
		command: `seq 1 2000 | while read -r n; do :; done; for item in alpha beta gamma; do printf '%s\n' "$item" >> "$SCRATCH_DIR/items"; done; sort "$SCRATCH_DIR/items" > result.txt; wc -l result.txt`,
	}})
	f.run(t)
	out := f.outcome(t, "scratch-loop")
	if out.State != "completed" || out.Status != 0 || out.NativeGaps != 0 || out.ExecutedChildren < 2000 || !strings.HasSuffix(out.Stdout, "3 result.txt\n") {
		t.Fatalf("scratch loop outcome = %+v", out)
	}
	if got := f.readFile(t, "result.txt"); got != "alpha\nbeta\ngamma\n" {
		t.Fatalf("result.txt = %q", got)
	}
	progs := f.programs(t)
	if len(progs) != 1 || progs[0].state != "completed" || progs[0].status != 0 {
		t.Fatalf("programs = %+v", progs)
	}
	if f.journalCount(t, "result.txt") != 1 {
		t.Fatalf("result journal count = %d", f.journalCount(t, "result.txt"))
	}
	execs := f.executions(t, progs[0].id)
	if !hasExec(execs, "sort", "completed") || !hasExec(execs, "wc", "completed") {
		t.Fatalf("executions = %+v", execs)
	}
}

func TestWorkflowRecommendedApprovalRunsExternalCommand(t *testing.T) {
	f := newCommandWorkflowFixtureWithApprovalRules(t, []workflowTurn{
		{callID: "approved-launch", command: `go env GOOS`},
	}, "  - category: command\n    pattern: \"go env*\"\n    effect: ask\n", false)
	ctx := t.Context()
	done := make(chan error, 1)
	go func() {
		_, err := f.h.Sessions.Manager.Submissions.Prompt(ctx, f.session, "execute workflow")
		done <- err
	}()
	kind := wire.CheckpointKindToolApproval
	var checkpoint wire.CheckpointEvent
	testutil.WaitFor(t, func() bool {
		pending, err := f.h.Sessions.Checkpoints.ListPending(ctx, f.session, &kind)
		if err == nil && len(pending) == 1 {
			checkpoint = pending[0]
			return true
		}
		return false
	})
	if checkpoint.ToolApproval == nil || checkpoint.ToolApproval.Plan.RecommendedOptionID == "" {
		t.Fatalf("approval plan = %+v", checkpoint.ToolApproval)
	}
	_, err := f.h.Sessions.Checkpoints.(*hitl.Checkpoints).Authority.ResolveApprovalOption(
		f.h.OwnerCtx(t, ctx), f.session, checkpoint.ID, checkpoint.ToolApproval.Plan.RecommendedOptionID)
	testutil.FailErr(t, "approve recommended option", err)
	testutil.FailErr(t, "complete workflow", <-done)
	out := f.outcome(t, "approved-launch")
	if out.State != "completed" || out.Status != 0 || out.NativeGaps != 0 || out.Stdout == "" {
		t.Fatalf("approved launch outcome = %+v", out)
	}
	pending, err := f.h.Sessions.Checkpoints.ListPending(ctx, f.session, &kind)
	testutil.FailErr(t, "list pending approvals", err)
	if len(pending) != 0 {
		t.Fatalf("approval cards left pending = %d", len(pending))
	}
	progs := f.programs(t)
	if len(progs) != 1 || !hasExec(f.executions(t, progs[0].id), "command", "completed") {
		t.Fatalf("approved launch receipts = %+v", progs)
	}
}

func TestWorkflowMoveAcrossAttachedRoots(t *testing.T) {
	f := newCommandWorkflowFixture(t, []workflowTurn{{
		callID:  "move-roots",
		command: `printf 'from scratch\n' > "$SCRATCH_DIR/source.txt"; mv "$SCRATCH_DIR/source.txt" @assets/arrived.txt; test ! -e "$SCRATCH_DIR/source.txt"; cat @assets/arrived.txt`,
	}})
	assets := t.TempDir()
	rootID := testdbseed.InsertProjectRoot(t, f.h.DB, testdbseed.DefaultProjectID, assets)
	_, err := f.h.DB.ExecContext(t.Context(), `UPDATE project_roots SET label = 'assets' WHERE id = ?`, rootID)
	testutil.FailErr(t, "label assets root", err)
	f.run(t)
	out := f.outcome(t, "move-roots")
	if out.State != "completed" || out.Status != 0 || out.NativeGaps != 0 || out.CompletedEffects != 2 || out.Stdout != "from scratch\n" {
		t.Fatalf("cross-root move outcome = %+v", out)
	}
	body, err := os.ReadFile(filepath.Join(assets, "arrived.txt"))
	testutil.FailErr(t, "read moved file", err)
	if string(body) != "from scratch\n" {
		t.Fatalf("moved bytes = %q", body)
	}
	progs := f.programs(t)
	if len(progs) != 1 || progs[0].status != 0 {
		t.Fatalf("programs = %+v", progs)
	}
	if !hasExec(f.executions(t, progs[0].id), "move", "completed") {
		t.Fatalf("move receipt = %+v", f.executions(t, progs[0].id))
	}
}

func TestWorkflowGitFreshRepositoryIgnoresUnrelatedNestedRepository(t *testing.T) {
	gitengine.TestingEnableBundledBinary()
	f := newCommandWorkflowFixture(t, []workflowTurn{
		{callID: "git-init", command: `git init; git status --short`},
		{callID: "git-commit", command: `git add README.txt; git commit -m 'Initial files'; git status --short`},
		{callID: "git-inspect", command: `git log -1 --format=%s; git show --format= --name-only HEAD`},
	})
	f.seed(t, "README.txt", "hello\n")
	nested := filepath.Join(f.dir, "vendor", "nested")
	testutil.FailErr(t, "create nested repository", os.MkdirAll(nested, 0o755))
	if output, err := exec.CommandContext(t.Context(), "git", "init", "-q", nested).CombinedOutput(); err != nil {
		t.Fatalf("initialize nested repository: %v: %s", err, output)
	}
	f.seed(t, "vendor/nested/file.txt", "unrelated\n")
	f.run(t)
	for _, callID := range []string{"git-init", "git-commit", "git-inspect"} {
		out := f.outcome(t, callID)
		if out.State != "completed" || out.Status != 0 || out.NativeGaps != 0 {
			t.Fatalf("%s outcome = %+v", callID, out)
		}
	}
	inspect := f.outcome(t, "git-inspect")
	if inspect.Stdout != "Initial files\nREADME.txt\n" {
		t.Fatalf("committed paths = %q", inspect.Stdout)
	}
	progs := f.programs(t)
	if len(progs) != 3 {
		t.Fatalf("programs = %+v", progs)
	}
	if !hasExec(f.executions(t, progs[1].id), "git add", "completed") || !hasExec(f.executions(t, progs[1].id), "git_commit", "completed") {
		t.Fatalf("git mutation receipts = %+v", f.executions(t, progs[1].id))
	}
}
