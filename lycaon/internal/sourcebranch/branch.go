// Package sourcebranch identifies independent lines of source history.
package sourcebranch

import (
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
)

// ID names one line of source history within a project.
type ID string

// Trunk is the project's primary physical workspace.
const Trunk ID = ""

const worktreePrefix = "worktree:"

// ForWorktree names a durable registered worktree.
func ForWorktree(worktreeID string) ID { return ID(worktreePrefix + worktreeID) }

// ForWorker returns the branch a write worker's overlay records against.
func ForWorker(jobID string) (ID, error) {
	id := strings.TrimSpace(jobID)
	if id == "" || strings.HasPrefix(id, worktreePrefix) {
		return Trunk, fmt.Errorf("worker branch requires a job id")
	}
	return ID(id), nil
}

// String returns the stored column value.
func (id ID) String() string { return string(id) }

// IsWorker reports whether the branch belongs to a write worker's overlay.
func (id ID) IsWorker() bool { return id != Trunk && !strings.HasPrefix(string(id), worktreePrefix) }

// Kind is the wire vocabulary describing the kind of tree this branch names.
func (id ID) Kind() api.SourceWorkspaceKind {
	if id.IsWorker() {
		return api.SourceWorkspaceKindWorker
	}
	return api.SourceWorkspaceKindProject
}

// FromKindAndJob resolves the branch a record with this wire kind belongs to.
// A worker kind without a job id is refused: filing it on the trunk would
// record overlay bytes as project history.
func FromKindAndJob(kind api.SourceWorkspaceKind, jobID string) (ID, error) {
	if kind == api.SourceWorkspaceKindWorker {
		return ForWorker(jobID)
	}
	if kind != api.SourceWorkspaceKindProject && strings.TrimSpace(string(kind)) != "" {
		return Trunk, fmt.Errorf("unknown source workspace kind %q", kind)
	}
	return Trunk, nil
}

// WorktreeID returns the durable binding identity for a worktree branch.
func (id ID) WorktreeID() (string, bool) {
	value, ok := strings.CutPrefix(string(id), worktreePrefix)
	return value, ok && value != ""
}
