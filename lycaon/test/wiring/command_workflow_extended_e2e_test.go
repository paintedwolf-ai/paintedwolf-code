package wiring

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkflowScratchLoopPublishesOnlyFinalResult(t *testing.T) {
	f := newCommandWorkflowFixture(t, []workflowTurn{{
		callID:  "scratch-loop",
		command: `for item in alpha beta gamma; do printf '%s\n' "$item" >> "$SCRATCH_DIR/items"; done; sort "$SCRATCH_DIR/items" > result.txt; wc -l result.txt`,
	}})
	f.run(t)
	out := f.outcome(t, "scratch-loop")
	if out.State != "completed" || out.Status != 0 || out.NativeGaps != 0 || !strings.HasSuffix(out.Stdout, "3 result.txt\n") {
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

func TestWorkflowGitFreshRepositoryIgnoresUnrelatedNestedRepository(t *testing.T) {
	f := newCommandWorkflowFixture(t, []workflowTurn{
		{callID: "git-init", command: `git init; git status --short`},
		{callID: "git-commit", command: `git add README.txt; git commit -m 'Initial files'; git status --short`},
		{callID: "git-inspect", command: `git log -1 --format=%s; git show --format= --name-only HEAD`},
	})
	f.seed(t, "README.txt", "hello\n")
	nested := filepath.Join(f.dir, "vendor", "nested", ".git")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
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
	if !hasExec(f.executions(t, progs[1].id), "git add", "completed") || !hasExec(f.executions(t, progs[1].id), "git commit", "completed") {
		t.Fatalf("git mutation receipts = %+v", f.executions(t, progs[1].id))
	}
}
