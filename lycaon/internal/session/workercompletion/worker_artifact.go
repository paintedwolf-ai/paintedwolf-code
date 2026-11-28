package workercompletion

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/pkg/api"
)

var (
	// ErrWorkspaceBaseRevisionMissing reports that no recorded base revision is
	// available to rule out commits in a clean Git checkout.
	ErrWorkspaceBaseRevisionMissing = errors.New("workspace base revision missing")
	// ErrWorkspaceCheckerUnavailable reports that the Git probe is unconfigured.
	ErrWorkspaceCheckerUnavailable = errors.New("git workspace checker unavailable")
)

// WorkspaceChangeObservation is the host inspection of a worker-owned baseline.
// Reported file names and primary-workspace dirt are not baseline observations.
type WorkspaceChangeObservation struct {
	Paths []string
	Err   error
}

// WorkspaceChangeChecker reports whether the worker's checkout differs from
// baseRevision through uncommitted changes or commits since baseRevision.
type WorkspaceChangeChecker interface {
	HasChanges(ctx context.Context, projectDir, baseRevision string) (bool, error)
}

// GitWorkspaceChangeChecker compares a Git checkout against the leg's recorded
// base revision (uncommitted changes or commits since the base).
type GitWorkspaceChangeChecker struct {
	Git git.GitManager
}

func (c *GitWorkspaceChangeChecker) HasChanges(ctx context.Context, projectDir, baseRevision string) (bool, error) {
	projectDir = strings.TrimSpace(projectDir)
	if c == nil || c.Git == nil || projectDir == "" {
		return false, ErrWorkspaceCheckerUnavailable
	}
	st, err := c.Git.Status(ctx, projectDir)
	if err != nil {
		return false, err
	}
	if st != nil && st.Dirty {
		return true, nil
	}
	baseRevision = strings.TrimSpace(baseRevision)
	if baseRevision == "" {
		return false, ErrWorkspaceBaseRevisionMissing
	}
	cmp, err := c.Git.Compare(ctx, projectDir, baseRevision, "HEAD")
	if err != nil {
		return false, err
	}
	return cmp.HeadOID != cmp.BaseOID, nil
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
func childHadImplementerArtifact(ctx context.Context, projectDir, baseRevision string, msgs []api.Message, wc WorkspaceChangeChecker, changes *WorkspaceChangeObservation) artifactProof {
	if childHadSuccessfulMutationTool(msgs) {
		return artifactPresent
	}
	if changes != nil {
		if changes.Err != nil {
			return artifactUndetermined
		}
		if len(changes.Paths) > 0 {
			return artifactPresent
		}
		return artifactAbsent
	}
	if wc == nil || strings.TrimSpace(projectDir) == "" {
		return artifactAbsent
	}
	if !childHadAnySuccessfulTool(msgs) {
		return artifactAbsent
	}
	changed, err := wc.HasChanges(ctx, projectDir, baseRevision)
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

func isImplementerAgent(agent string) bool {
	return strings.TrimSpace(agent) == orchestration.ProfileImplementer
}
