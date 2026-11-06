package gitcmd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

var bisectTemplate = repoTemplate{name: "bisect"}

// newBisectHarness builds twelve commits where commit 7 first records a bad
// state, with check.sh as the untracked check Git itself can run.
func newBisectHarness(t *testing.T) *harness {
	return bisectTemplate.materialize(t, func(h *harness) {
		seedEmptyRepo(h)
		for i := 1; i <= 12; i++ {
			state := "good\n"
			if i >= 7 {
				state = "bad\n"
			}
			h.write("state", state)
			h.write("n", strings.Repeat("x", i)+"\n")
			h.git("add", "--", "state", "n")
			h.git("commit", "-q", "-m", "commit "+strings.Repeat("I", i))
		}
		h.write("check.sh", "grep -q good state\n")
	})
}

// checkNested stands in for program dispatch: it answers each check from the
// checked-out state file, and can stop the run at a chosen call.
type checkNested struct {
	root     string
	calls    int
	stopAt   int
	verdict  func(state, n string) int
	commands []string
}

func (c *checkNested) RunNested(_ context.Context, _ commandprogram.OperationRequest, argv commandprogram.Fields, _ map[string]string, _ commandprogram.Streams) (toolcontract.ProducerResult, error) {
	c.calls++
	c.commands = append(c.commands, strings.Join(argv.Values(), " "))
	if c.calls == c.stopAt {
		return toolcontract.ProducerResult{Status: 130, Terminal: toolcontract.ProducerInterrupted, EffectCertainty: toolcontract.NoEffect}, context.Canceled
	}
	state, err := os.ReadFile(filepath.Join(c.root, "state"))
	if err != nil {
		return toolcontract.ProducerResult{}, err
	}
	n, err := os.ReadFile(filepath.Join(c.root, "n"))
	if err != nil {
		return toolcontract.ProducerResult{}, err
	}
	status := 1
	if c.verdict != nil {
		status = c.verdict(strings.TrimSpace(string(state)), strings.TrimSpace(string(n)))
	} else if strings.TrimSpace(string(state)) == "good" {
		status = 0
	}
	return toolcontract.ProducerResult{Status: status, Terminal: toolcontract.ProducerCompleted, Complete: true, EffectCertainty: toolcontract.NoEffect}, nil
}

func (h *harness) bisectFile(name string) (string, bool) {
	data, err := os.ReadFile(filepath.Join(h.root, ".git", name))
	return string(data), err == nil
}

func TestBisectRunNamesTheFirstBadCommitAndMatchesGit(t *testing.T) {
	t.Parallel()
	h := newBisectHarness(t)
	reference := newBisectHarness(t)
	h.nested = &checkNested{root: h.root}
	culprit := strings.TrimSpace(h.git("rev-parse", "HEAD~5"))
	start := h.exec("git", "bisect", "start", "HEAD", "HEAD~11")
	testutil.FailErr(t, "start", start.err)
	wantStart, _, _ := reference.direct(reference.root, "bisect", "start", "HEAD", "HEAD~11")
	if start.stdout != wantStart || start.result.Status != 0 || start.result.EffectCertainty != toolcontract.EffectCompleted {
		t.Fatalf("start %q want %q result %+v", start.stdout, wantStart, start.result)
	}
	run := h.exec("git", "bisect", "run", "sh", "./check.sh")
	testutil.FailErr(t, "run", run.err)
	wantRun, _, wantStatus := reference.direct(reference.root, "bisect", "run", "sh", "./check.sh")
	if run.stdout != wantRun || run.result.Status != wantStatus {
		t.Fatalf("run stdout %q\nwant %q\nstatus %d want %d", run.stdout, wantRun, run.result.Status, wantStatus)
	}
	if !strings.HasPrefix(run.stdout, "running 'sh' './check.sh'\n") || !strings.Contains(run.stdout, culprit+" is the first bad commit\n") {
		t.Fatalf("run output %q does not name %s", run.stdout, culprit)
	}
	for _, command := range h.nested.(*checkNested).commands {
		if command != "sh ./check.sh" {
			t.Fatalf("dispatched check %q", command)
		}
	}
	got, _ := h.bisectFile("BISECT_LOG")
	want, _ := reference.bisectFile("BISECT_LOG")
	if got != want || h.exec("git", "bisect", "log").stdout != want {
		t.Fatalf("log %q\nwant %q", got, want)
	}
	if status := h.git("status"); !strings.Contains(status, "You are currently bisecting, started from branch 'main'.") {
		t.Fatalf("status while bisecting %q", status)
	}
	reset := h.exec("git", "bisect", "reset")
	testutil.FailErr(t, "reset", reset.err)
	if reset.result.Status != 0 || strings.TrimSpace(h.git("symbolic-ref", "--short", "HEAD")) != "main" {
		t.Fatalf("reset %+v %q", reset.result, reset.stderr)
	}
	if _, active := h.bisectFile("BISECT_START"); active || h.git("for-each-ref", "refs/bisect") != "" {
		t.Fatal("reset left the session behind")
	}
}

func TestBisectRunSkipsStatus125(t *testing.T) {
	t.Parallel()
	h := newBisectHarness(t)
	check := &checkNested{root: h.root, verdict: func(state, n string) int {
		if len(n) == 9 {
			return 125
		}
		if state == "good" {
			return 0
		}
		return 1
	}}
	h.nested = check
	culprit := strings.TrimSpace(h.git("rev-parse", "HEAD~5"))
	testutil.FailErr(t, "start", h.exec("git", "bisect", "start", "HEAD", "HEAD~11").err)
	run := h.exec("git", "bisect", "run", "sh", "./check.sh")
	testutil.FailErr(t, "run", run.err)
	log, _ := h.bisectFile("BISECT_LOG")
	if run.result.Status != 0 || !strings.Contains(run.stdout, culprit+" is the first bad commit\n") || !strings.Contains(log, "# skip: [") {
		t.Fatalf("run %+v stdout %q log %q", run.result, run.stdout, log)
	}
}

func TestBisectRefusesADirtyTreeAndRunsInAWorktree(t *testing.T) {
	t.Parallel()
	h := newBisectHarness(t)
	h.write("state", "edited\n")
	h.exec("git", "bisect", "start", "HEAD", "HEAD~11").refused(t, "dirty start", "GIT_BISECT_DIRTY_TREE")
	if _, active := h.bisectFile("BISECT_START"); active {
		t.Fatal("refused start recorded a session")
	}
	worktree := filepath.Join(h.root, "bisect-tree")
	h.git("worktree", "add", "-q", "--detach", worktree, "HEAD")
	var err error
	h.paths, err = projectpaths.NewProgramPaths(t.Context(), h.owners.Boundary, h.tc, worktree)
	testutil.FailErr(t, "worktree paths", err)
	h.nested = &checkNested{root: worktree}
	testutil.FailErr(t, "start in worktree", h.exec("git", "bisect", "start", "HEAD", "HEAD~11").err)
	run := h.exec("git", "bisect", "run", "sh", "./check.sh")
	testutil.FailErr(t, "run in worktree", run.err)
	culprit := strings.TrimSpace(h.git("rev-parse", "HEAD~5"))
	if run.result.Status != 0 || !strings.Contains(run.stdout, culprit+" is the first bad commit\n") {
		t.Fatalf("worktree run %+v %q", run.result, run.stdout)
	}
	if h.read("state") != "edited\n" {
		t.Fatal("the worktree bisect changed the main tree")
	}
}

func TestBisectRunInterruptionLeavesARecordedSession(t *testing.T) {
	t.Parallel()
	h := newBisectHarness(t)
	h.nested = &checkNested{root: h.root, stopAt: 2}
	testutil.FailErr(t, "start", h.exec("git", "bisect", "start", "HEAD", "HEAD~11").err)
	run := h.exec("git", "bisect", "run", "sh", "./check.sh")
	if run.result.Terminal != toolcontract.ProducerInterrupted || run.result.EffectCertainty != toolcontract.EffectCompleted {
		t.Fatalf("interrupted run %+v err %v", run.result, run.err)
	}
	head := strings.TrimSpace(h.git("rev-parse", "HEAD"))
	expected, _ := h.bisectFile("BISECT_EXPECTED_REV")
	if strings.TrimSpace(expected) != head || !strings.Contains(run.stderr, "while testing "+head) || !strings.Contains(run.stderr, "Run git bisect reset") {
		t.Fatalf("stop report %q head %s expected %q", run.stderr, head, expected)
	}
	if status := h.git("status"); !strings.Contains(status, "You are currently bisecting") {
		t.Fatalf("status after interruption %q", status)
	}
}

// TestBisectManualStepsMatchGit drives custom terms, verdicts, terms and the
// one-line view through both implementations on identical repositories.
func TestBisectManualStepsMatchGit(t *testing.T) {
	t.Parallel()
	h := newBisectHarness(t)
	reference := newBisectHarness(t)
	for _, line := range []string{
		"git bisect start --term-old=fast --term-new=slow HEAD HEAD~11 -- n",
		"git bisect terms",
		"git bisect terms --term-old",
		"git bisect fast",
		"git bisect visualize --oneline",
		"git bisect slow",
		"git bisect skip",
		"git bisect log",
	} {
		got := h.exec(strings.Fields(line)...)
		testutil.FailErr(t, line, got.err)
		stdout, _, status := reference.direct(reference.root, strings.Fields(line)[1:]...)
		if got.stdout != stdout || got.result.Status != status {
			t.Fatalf("%s:\nstdout %q\nwant   %q\nstatus %d want %d", line, got.stdout, stdout, got.result.Status, status)
		}
	}
	for _, name := range []string{"BISECT_START", "BISECT_TERMS", "BISECT_NAMES", "BISECT_LOG", "BISECT_EXPECTED_REV"} {
		got, _ := h.bisectFile(name)
		want, _ := reference.bisectFile(name)
		if got != want {
			t.Fatalf("%s %q want %q", name, got, want)
		}
	}
	if got, want := h.git("for-each-ref", "refs/bisect"), reference.git("for-each-ref", "refs/bisect"); got != want {
		t.Fatalf("bisect refs %q want %q", got, want)
	}
}

func TestBisectRefusesUnsupportedForms(t *testing.T) {
	t.Parallel()
	h := newBisectHarness(t)
	for _, c := range []struct {
		argv []string
		code string
	}{
		{[]string{"git", "bisect", "replay", "log.txt"}, "COMMAND_NATIVE_FORM_UNSUPPORTED"},
		{[]string{"git", "bisect", "visualize"}, "COMMAND_OPERAND_INVALID"},
		{[]string{"git", "bisect", "view", "--stat"}, "COMMAND_OPTION_UNSUPPORTED"},
		{[]string{"git", "bisect", "run"}, "COMMAND_OPERAND_INVALID"},
	} {
		h.exec(c.argv...).refused(t, strings.Join(c.argv, " "), c.code)
	}
	got := h.exec("git", "bisect", "good")
	stdout, stderr, status := h.direct(h.root, "bisect", "good")
	if got.result.Status != status || got.stdout != stdout || got.stderr != stderr {
		t.Fatalf("verdict outside a session %+v %q %q, want %d %q %q", got.result, got.stdout, got.stderr, status, stdout, stderr)
	}
	testutil.FailErr(t, "start", h.exec("git", "bisect", "start", "HEAD", "HEAD~11").err)
	h.exec("git", "bisect", "run", "sh", "./check.sh").refused(t, "run without dispatch", "COMMAND_NATIVE_FORM_UNSUPPORTED")
}
