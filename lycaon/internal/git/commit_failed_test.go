package git_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestCommitFailedKeepsOutputTail(t *testing.T) {
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

func TestMarshalCommitToolResponseHookFailure(t *testing.T) {
	raw := strings.Repeat("noise\n", 100) +
		"task: [build] go build ./...\n" +
		"# github.com/lycaon/lycaon/internal/git\n" +
		"./commit_failed.go:1: undefined: boom\n" +
		"task: Failed to run task \"build\": exit status 1\n"
	out, err := git.MarshalCommitToolResponse("", git.CommitStaging{}, git.NewCommitFailed([]byte(raw), 17))
	var failed *git.CommitFailed
	if !errors.As(err, &failed) || failed.ExitCode != 17 {
		t.Fatalf("commit diagnostics lost typed failure: %v", err)
	}
	var resp git.CommitToolResponse
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		testutil.FailErr(t, "unmarshal JSON document", err)
	}
	if resp.Available {
		t.Fatal("available want false")
	}
	if resp.ExitCode != 17 {
		t.Fatalf("exit_code = %d", resp.ExitCode)
	}
	if resp.Error != "git commit failed" {
		t.Fatalf("error = %q", resp.Error)
	}
	if !strings.Contains(resp.OutputTail, "undefined: boom") {
		t.Fatalf("output_tail missing compile error: %q", resp.OutputTail)
	}
	if lines := strings.Count(resp.OutputTail, "\n") + 1; lines > 80 {
		t.Fatalf("output_tail lines = %d", lines)
	}
	if !strings.Contains(resp.Hint, "Git or hook diagnostics") {
		t.Fatalf("hint = %q", resp.Hint)
	}
	if resp.OutputTail == "task: [build] go build ./..." {
		t.Fatal("output_tail was crushed to Task echo only")
	}
}

func TestCommitFailedDoesNotInferHookFromOutput(t *testing.T) {
	cf := git.NewCommitFailed([]byte("pre-commit hook failed\nsome local lint\n"), 1)
	if !strings.Contains(cf.Hint(), "Git or hook diagnostics") {
		t.Fatalf("hint = %q", cf.Hint())
	}
	if cf.Summary() != "git commit failed" {
		t.Fatalf("summary = %q", cf.Summary())
	}
}

func TestCommitReceiptRetainsSuccessfulHashWhenHistoryFails(t *testing.T) {
	failure := errors.New("record Git history: unavailable")
	raw, err := git.MarshalCommitToolResponse("committed", git.CommitStaging{Paths: []string{"a.txt"}}, failure)
	if !errors.Is(err, failure) {
		t.Fatalf("lost history failure: %v", err)
	}
	var response git.CommitToolResponse
	testutil.FailErr(t, "decode committed receipt", json.Unmarshal([]byte(raw), &response))
	if !response.Available || response.Hash != "committed" || response.Error == "" || len(response.Paths) != 1 {
		t.Fatalf("lost successful commit: %+v", response)
	}
}
