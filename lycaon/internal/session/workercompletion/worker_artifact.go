package workercompletion

import (
	"context"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"log/slog"
	"strings"

	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/pkg/api"
)

// WorkspaceChangeChecker reports whether the project tree changed during a worker run.
type WorkspaceChangeChecker interface {
	HasChanges(ctx context.Context, projectDir string) (bool, error)
}

// GitWorkspaceChangeChecker uses git status when the project is a repository.
type GitWorkspaceChangeChecker struct {
	Git git.GitManager
}

func (c *GitWorkspaceChangeChecker) HasChanges(ctx context.Context, projectDir string) (bool, error) {
	if c == nil || c.Git == nil || strings.TrimSpace(projectDir) == "" {
		return false, nil
	}
	st, err := c.Git.Status(ctx, projectDir)
	if err != nil {
		return false, err
	}
	if st == nil {
		return false, nil
	}
	return st.Dirty, nil
}

// artifactProof is the implementer artifact verdict. "Ran and found nothing"
// and "could not run" are different facts.
type artifactProof int

const (
	// artifactAbsent means the probe ran and found no artifact.
	artifactAbsent artifactProof = iota
	// artifactPresent means a mutation ledger or workspace change proves one.
	artifactPresent
	// artifactUndetermined means the workspace probe could not answer.
	artifactUndetermined
)

// childHadImplementerArtifact checks for mutation ledger records or workspace modifications.
func childHadImplementerArtifact(ctx context.Context, projectDir string, msgs []api.Message, wc WorkspaceChangeChecker) artifactProof {
	if childHadSuccessfulMutationTool(msgs) {
		return artifactPresent
	}
	if wc == nil || strings.TrimSpace(projectDir) == "" {
		return artifactAbsent
	}
	if !childHadAnySuccessfulTool(msgs) {
		return artifactAbsent
	}
	changed, err := wc.HasChanges(ctx, projectDir)
	if err != nil {
		slog.WarnContext(ctx, "worker artifact workspace probe failed; artifact undetermined",
			"component", "worker_completion", "project_dir", projectDir, "error", err)
		return artifactUndetermined
	}
	if changed {
		return artifactPresent
	}
	return artifactAbsent
}

// childHadSuccessfulMutationTool scans history for a successful file-content mutation.
func childHadSuccessfulMutationTool(msgs []api.Message) bool {
	pending := map[string]string{}
	for _, msg := range msgs {
		switch msg.Role {
		case api.MessageRoleAssistant:
			pending = map[string]string{}
			for _, tc := range msg.ToolCalls {
				id := strings.TrimSpace(tc.ID)
				if id == "" {
					continue
				}
				pending[id] = strings.TrimSpace(tc.Name)
			}
		case api.MessageRoleTool:
			if msg.ToolResult == nil {
				continue
			}
			id := strings.TrimSpace(msg.ToolResult.ToolCallID)
			name, called := pending[id]
			delete(pending, id)
			if called && !toolMessageFailed(msg) && (toolcontract.MutatesContent(name) || name == "restore_version") {
				return true
			}
		case api.MessageRoleUser, api.MessageRoleSystem:
		}
	}
	return false
}

// childHadAnySuccessfulTool returns true when the worker executed at least one
// tool call that did not return a failure result.
func childHadAnySuccessfulTool(msgs []api.Message) bool {
	hasPending := false
	for _, msg := range msgs {
		switch msg.Role {
		case api.MessageRoleAssistant:
			hasPending = false
			for _, tc := range msg.ToolCalls {
				if strings.TrimSpace(tc.ID) == "" {
					continue
				}
				hasPending = true
			}
		case api.MessageRoleTool:
			if !hasPending {
				continue
			}
			if !toolMessageFailed(msg) {
				return true
			}
			hasPending = false
		case api.MessageRoleUser, api.MessageRoleSystem:
		}
	}
	return false
}

// CompositeWorkspaceChangeChecker combines git dirty checks with non-git directory files.
type CompositeWorkspaceChangeChecker struct {
	Git *GitWorkspaceChangeChecker
}

func (c *CompositeWorkspaceChangeChecker) HasChanges(ctx context.Context, projectDir string) (bool, error) {
	if c != nil && c.Git != nil {
		if ok, err := c.Git.HasChanges(ctx, projectDir); err == nil && ok {
			return true, nil
		}
	}
	return dirHasWorkspaceFiles(projectDir)
}

func isImplementerAgent(agent string) bool {
	return strings.TrimSpace(agent) == orchestration.ProfileImplementer
}
