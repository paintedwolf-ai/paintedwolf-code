package projectsource

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/sourceeffect"
	"github.com/lycaon/lycaon/pkg/api"
)

// RemoveEntry records an undoable agent deletion without using system Trash.
func (s *SourceMutationService) RemoveEntry(ctx context.Context, removal sourceeffect.Removal) (string, error) {
	record := removal.Record
	if s == nil || record.ProjectID == "" || record.Op != api.SourceChangeOpDelete || strings.TrimSpace(removal.RootPath) == "" {
		return "", fmt.Errorf("agent removal requires a project, a root, and a delete operation")
	}
	if record.BranchID.IsWorker() {
		return "", fmt.Errorf("agent removal on a worker branch belongs to its overlay")
	}
	operationID := record.OperationID
	if operationID == "" {
		operationID = uuid.NewString()
	}
	digest, err := sourceMutationDigest(struct {
		ProjectID, BranchID, RootID, RootPath, Path, SessionID, ToolCallID, ReviewedSHA256 string
		Recursive, Unrecoverable                                                           bool
	}{
		record.ProjectID, record.BranchID.String(), record.RootID, removal.RootPath, record.Path,
		record.SessionID, record.ToolCallID, removal.ReviewedSHA256, removal.Recursive, removal.Unrecoverable,
	})
	if err != nil {
		return "", err
	}
	_, err = s.execute(ctx, operationID, record.ProjectID, digest, func() (*sourceMutationPlan, error) {
		return prepareAgentRemoval(ctx, operationID, removal)
	})
	if err == nil {
		s.reclaimEvictedRecovery(ctx)
	}
	return operationID, err
}

func prepareAgentRemoval(ctx context.Context, operationID string, removal sourceeffect.Removal) (*sourceMutationPlan, error) {
	record := removal.Record
	// Tool path resolution excludes repository metadata and control-plane paths.
	// Reviewed removals can reach the project overlay.
	abs, rel, err := resolveLifecyclePath(removal.RootPath, record.Path)
	if err != nil {
		return nil, err
	}
	if isSourceRootRel(rel) {
		return nil, ErrSourcePathInvalid
	}
	info, err := os.Lstat(abs)
	if os.IsNotExist(err) {
		return nil, ErrSourceNotFound
	}
	if err != nil {
		return nil, err
	}
	// Review covers the selected entry, not its children, unless recursive.
	if info.IsDir() && !removal.Recursive {
		entries, readErr := os.ReadDir(abs)
		if readErr != nil {
			return nil, readErr
		}
		if len(entries) > 0 {
			return nil, ErrSourceNotEmpty
		}
	}
	evidence, err := sourceHistoryEvidence(ctx, abs)
	if err != nil {
		return nil, err
	}
	if evidence.sha256 != removal.ReviewedSHA256 {
		return nil, fmt.Errorf("%w: the file changed after it was reviewed", ErrSourceWriteConflict)
	}
	beforeSize := evidence.size
	if beforeSize == 0 && record.BeforeSize > 0 {
		beforeSize = record.BeforeSize
	}
	recovery := sourceMutationRecovery{Disposal: sourceDisposalDiscard, RecoveryID: operationID}
	var pub sourceMutationPublication
	if removal.Unrecoverable {
		recovery = sourceMutationRecovery{Disposal: sourceDisposalDiscard, Unrecoverable: true}
		pub.EntryIdentity, err = fspath.EntryIdentity(abs)
		if err != nil {
			return nil, err
		}
	}
	return &sourceMutationPlan{
		sourceMutationContent:     sourceMutationContent{Before: evidence.content, BaseSHA256: evidence.sha256, BeforeSize: beforeSize},
		sourceMutationPublication: pub,
		sourceMutationRecovery:    recovery,
		sourceMutationAttribution: sourceMutationAttribution{ProjectID: record.ProjectID, WorkspaceID: removal.Change.WorkspaceID, BranchID: record.BranchID, SessionID: record.SessionID, Turn: record.Turn, Agent: &sourceMutationAgent{
			JobID: record.JobID, ToolCallID: record.ToolCallID, ToolName: record.ToolName,
			WorkspaceKind: removal.Change.WorkspaceKind,
		}},
		Kind:      "delete",
		RootID:    record.RootID,
		RootPath:  removal.RootPath,
		Path:      rel,
		AbsPath:   abs,
		Recursive: removal.Recursive,
		EntryKind: sourceEntryKind(info),
		Changed:   true,
		Response:  json.RawMessage(`{}`),
	}, nil
}
