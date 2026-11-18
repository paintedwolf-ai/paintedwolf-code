package git_test

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/git"
)

func TestCommitFailedKeepsOutputTail(t *testing.T) {
	t.Parallel()
	raw := "task: [build] go build ./...\n" +
		"pattern ./...: directory prefix . does not contain main module\n" +
		"task: Failed to run task \"build\": exit status 1\n"
	cf := git.NewCommitFailed([]byte(raw), 1)
	if !strings.Contains(cf.Tail(), "does not contain main module") {
		t.Fatalf("tail missing compiler detail: %q", cf.Tail())
	}
	if !strings.Contains(cf.Error(), "does not contain main module") {
		t.Fatalf("Error() crushed to first line: %q", cf.Error())
	}
	if hint := cf.Hint(); !strings.Contains(hint, "output_tail") {
		t.Fatalf("hint = %q", hint)
	}
	if git.NormalizeToolError(cf) != "git commit failed" {
		t.Fatalf("NormalizeToolError = %q", git.NormalizeToolError(cf))
	}
	if cf.ExitCode != 1 {
		t.Fatalf("ExitCode = %d", cf.ExitCode)
	}
}

func TestCommitFailedDoesNotInferHookFromOutput(t *testing.T) {
	t.Parallel()
	cf := git.NewCommitFailed([]byte("pre-commit hook failed\nsome local lint\n"), 1)
	if !strings.Contains(cf.Hint(), "Git or hook diagnostics") {
		t.Fatalf("hint = %q", cf.Hint())
	}
	if cf.Summary() != "git commit failed" {
		t.Fatalf("summary = %q", cf.Summary())
	}
}
