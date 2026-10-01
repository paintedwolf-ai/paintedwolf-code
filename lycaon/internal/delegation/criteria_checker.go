package delegation

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/inspector"
)

// CriteriaChecker resolves file:modified and test:pass without coordinator chat.
type CriteriaChecker interface {
	FileModified(ctx context.Context, projectDir, delegationBaseSHA, path string) (bool, error)
	TestPass(ctx context.Context, delegationID, taskID, command string) (bool, error)
}

// GitInspectorCriteriaChecker implements CriteriaChecker using git diff and inspector verify JSONL.
type GitInspectorCriteriaChecker struct {
	Git       *git.Manager
	Inspector inspector.Inspector
	Store     Store
}

// FileModified reports whether path differs from delegation base HEAD.
func (c *GitInspectorCriteriaChecker) FileModified(ctx context.Context, projectDir, delegationBaseSHA, path string) (bool, error) {
	if c == nil || c.Git == nil {
		return false, nil
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return false, nil
	}
	diff, err := c.Git.Diff(ctx, projectDir, git.GitDiffOpts{Paths: []string{path}})
	if err != nil {
		return false, err
	}
	if strings.TrimSpace(diff) != "" {
		return true, nil
	}
	if delegationBaseSHA != "" {
		return c.Git.DiffFromRef(ctx, projectDir, delegationBaseSHA, path)
	}
	return false, nil
}

// TestPass reports whether verify evidence exists with exit_code 0 for the command.
func (c *GitInspectorCriteriaChecker) TestPass(ctx context.Context, delegationID, taskID, command string) (bool, error) {
	if c == nil || c.Inspector == nil {
		return false, nil
	}
	status, err := c.Inspector.GetStatus(ctx, delegationID, taskID)
	if err != nil || status == nil {
		return false, err
	}
	rec := status.Evidence[evidence.GateTypeVerify]
	if rec == nil {
		return false, nil
	}
	got, _ := rec.Artifacts["command"].(string)
	if !commandsurface.SameCommandLine(got, command) {
		return false, nil
	}
	return inspector.ExitCodePassed(rec.Artifacts), nil
}
