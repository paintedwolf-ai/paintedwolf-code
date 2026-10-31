package projectsource

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"strings"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/sourceeffect"
	"github.com/lycaon/lycaon/pkg/api"
)

// CanMoveAside reports whether location can be moved aside into its root's
// private recovery directory on the same filesystem.
func (s *SourceMutationService) CanMoveAside(ctx context.Context, location fseffect.Location) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	abs, _, err := resolveLifecyclePath(location.Root, location.Rel)
	if err != nil {
		return false, err
	}
	available, err := s.Effects.canMoveAside(location.Root, abs)
	if err != nil {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		slog.WarnContext(ctx, "recovery capability unavailable", "error", err)
		return false, nil
	}
	return available, nil
}

// InspectRemoval measures metadata and kernel entry identity without reading file contents.
func (s *SourceMutationService) InspectRemoval(ctx context.Context, location fseffect.Location, memory sourceeffect.Memory) (sourceeffect.RemovalSnapshot, error) {
	abs, _, err := resolveLifecyclePath(location.Root, location.Rel)
	if err != nil {
		return sourceeffect.RemovalSnapshot{}, err
	}
	identity, err := fspath.EntryIdentity(abs)
	if err != nil {
		return sourceeffect.RemovalSnapshot{}, err
	}
	sha, size, err := measureRemovalMetadata(ctx, abs, memory)
	if err != nil {
		return sourceeffect.RemovalSnapshot{}, err
	}
	if err := requireSourceIdentity(abs, identity); err != nil {
		return sourceeffect.RemovalSnapshot{}, err
	}
	available, err := s.Effects.canMoveAside(location.Root, abs)
	if err != nil {
		if ctx.Err() != nil {
			return sourceeffect.RemovalSnapshot{}, ctx.Err()
		}
		slog.WarnContext(ctx, "recovery capability unavailable", "error", err)
		available = false
	}
	return sourceeffect.RemovalSnapshot{
		Identity:          identity,
		TreeSHA:           sha,
		Bytes:             size.Bytes,
		Files:             size.Files,
		RecoveryAvailable: available,
		RecoveryBytes:     size.AllocatedBytes,
		RecoverySizeKnown: size.AllocationKnown,
	}, nil
}

func measureRemovalMetadata(ctx context.Context, abs string, memory sourceeffect.Memory) (digest string, size sourceTreeSize, measureErr error) {
	if err := ctx.Err(); err != nil {
		return "", sourceTreeSize{}, err
	}
	if memory == nil {
		return "", sourceTreeSize{}, errors.New("source operation memory is required")
	}
	info, err := os.Lstat(abs)
	if err != nil {
		return "", sourceTreeSize{}, err
	}
	hash := sha256.New()
	allocation := &sourceTreeAllocation{known: true, memory: memory}
	defer func() { measureErr = errors.Join(measureErr, allocation.close()) }()
	recordEntry := func(rel string, entryInfo fs.FileInfo, readlink func(string) (string, error)) error {
		if err := allocation.observe(ctx, entryInfo); err != nil {
			return err
		}
		if !entryInfo.IsDir() {
			size.Files++
			if entryInfo.Mode().IsRegular() {
				size.Bytes += entryInfo.Size()
			}
		}
		link := ""
		if entryInfo.Mode()&os.ModeSymlink != 0 {
			var err error
			link, err = readlink(rel)
			if err != nil {
				return err
			}
		}
		_, err := fmt.Fprintf(hash, "%s\x00%d\x00%d\x00%d\x00%s\n", rel, uint32(entryInfo.Mode()), entryInfo.Size(), entryInfo.ModTime().UTC().UnixNano(), link)
		return err
	}
	if !info.IsDir() {
		if err := recordEntry(".", info, func(string) (string, error) { return os.Readlink(abs) }); err != nil {
			return "", sourceTreeSize{}, err
		}
	} else {
		root, err := os.OpenRoot(abs)
		if err != nil {
			return "", sourceTreeSize{}, err
		}
		defer func() { _ = root.Close() }()
		opened, err := root.Stat(".")
		if err != nil {
			return "", sourceTreeSize{}, err
		}
		if !os.SameFile(info, opened) {
			return "", sourceTreeSize{}, ErrSourceMutationDiverged
		}
		if err := walkSourceTree(ctx, root, ".", memory, func(rel string, entryInfo fs.FileInfo) error {
			return recordEntry(rel, entryInfo, root.Readlink)
		}); err != nil {
			return "", sourceTreeSize{}, err
		}
	}
	size.AllocatedBytes = allocation.bytes
	size.AllocationKnown = allocation.known
	return hex.EncodeToString(hash.Sum(nil)), size, nil
}

func requireRemovalMetadata(ctx context.Context, path, want string, memory sourceeffect.Memory) error {
	got, _, err := measureRemovalMetadata(ctx, path, memory)
	if err != nil {
		return err
	}
	if got != want {
		return ErrSourceMutationDiverged
	}
	return nil
}

// RemoveEntry records an undoable agent deletion without using system Trash.
func (s *SourceMutationService) RemoveEntry(ctx context.Context, removal sourceeffect.Removal) (string, error) {
	if removal.Memory == nil {
		return "", fmt.Errorf("agent removal memory is required")
	}
	record := removal.Record
	if s == nil || record.ProjectID == "" || record.Op != api.SourceChangeOpDelete || strings.TrimSpace(removal.RootPath) == "" {
		return "", fmt.Errorf("agent removal requires a project, a root, and a delete operation")
	}
	if record.BranchID.IsWorker() {
		return "", fmt.Errorf("agent removal on a worker branch belongs to its overlay")
	}
	releaseHistory, err := s.Paths.lockSourceHistory(ctx, record.ProjectID)
	if err != nil {
		return "", err
	}
	defer releaseHistory()
	operationID := record.OperationID
	if operationID == "" {
		operationID = uuid.NewString()
	}
	digest, err := sourceMutationDigest(struct {
		ProjectID, BranchID, RootID, RootPath, Path, SessionID, ToolCallID, ReviewedSHA256 string
		Recursive, Unrecoverable                                                           bool
		ReviewedFiles                                                                      []sourceeffect.ReviewedFile
		Snapshot                                                                           *sourceeffect.RemovalSnapshot
	}{
		record.ProjectID, record.BranchID.String(), record.RootID, removal.RootPath, record.Path,
		record.SessionID, record.ToolCallID, removal.ReviewedSHA256, removal.Recursive, removal.Unrecoverable, removal.ReviewedFiles, removal.ReviewedSnapshot,
	})
	if err != nil {
		return "", err
	}
	_, err = s.execute(ctx, operationID, record.ProjectID, digest, removal.Memory, func() (*sourceMutationPlan, error) {
		return s.prepareAgentRemoval(ctx, operationID, removal)
	})
	return operationID, err
}

func (s *SourceMutationService) prepareAgentRemoval(ctx context.Context, operationID string, removal sourceeffect.Removal) (*sourceMutationPlan, error) {
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
		if err := requireEmptyRemovalDirectory(ctx, fseffect.Location{Root: removal.RootPath, Rel: rel}, info, removal.Memory); err != nil {
			return nil, err
		}
	}
	if removal.ReviewedSnapshot != nil {
		current, err := s.InspectRemoval(ctx, fseffect.Location{Root: removal.RootPath, Rel: rel}, removal.Memory)
		if err != nil {
			return nil, err
		}
		if current != *removal.ReviewedSnapshot {
			return nil, ErrSourceMutationDiverged
		}
	}
	for _, reviewed := range removal.ReviewedFiles {
		childAbs, _, err := resolveLifecyclePath(removal.RootPath, reviewed.Rel)
		if err != nil {
			return nil, err
		}
		childEvidence, err := sourceHistoryEvidence(ctx, childAbs)
		if err != nil {
			return nil, err
		}
		if childEvidence.sha256 != reviewed.SHA256 {
			return nil, fmt.Errorf("%w: the file changed after it was reviewed", ErrSourceWriteConflict)
		}
	}
	var evidence sourceEvidence
	switch {
	case info.IsDir():
		evidence = sourceEvidence{entryKind: SourceEntryFolder}
	case removal.ReviewedSHA256 != "":
		evidence, err = sourceHistoryEvidence(ctx, abs)
		if err != nil {
			return nil, err
		}
		if evidence.sha256 != removal.ReviewedSHA256 {
			return nil, fmt.Errorf("%w: the file changed after it was reviewed", ErrSourceWriteConflict)
		}
	default:
		evidence = sourceEvidence{entryKind: SourceEntryFile, size: info.Size()}
	}
	beforeSize := evidence.size
	if beforeSize == 0 && record.BeforeSize > 0 {
		beforeSize = record.BeforeSize
	}
	identity, err := fspath.EntryIdentity(abs)
	if err != nil {
		return nil, err
	}
	pub := sourceMutationPublication{EntryIdentity: identity}
	var recovery sourceMutationRecovery
	if removal.Unrecoverable {
		recovery = sourceMutationRecovery{Disposal: sourceDisposalDiscard, Unrecoverable: true}
		if snapshot := removal.ReviewedSnapshot; snapshot != nil {
			pub.EntryIdentity, recovery.TreeSHA, beforeSize = snapshot.Identity, snapshot.TreeSHA, snapshot.Bytes
		}
	} else {
		available, sameErr := s.Effects.canMoveAside(removal.RootPath, abs)
		if sameErr != nil || !available {
			if sameErr == nil {
				sameErr = errors.ErrUnsupported
			}
			return nil, &sourceeffect.RecoveryUnavailableError{Cause: sameErr}
		}
		recovery = sourceMutationRecovery{
			Disposal:         sourceDisposalDiscard,
			RecoveryID:       operationID,
			EntryRecoveryKey: operationID,
			NativeTrash:      &sourceTrashRecovery{RecoveryKey: operationID},
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

// One observed child is sufficient to refuse nonrecursive directory removal.
func requireEmptyRemovalDirectory(ctx context.Context, location fseffect.Location, selected os.FileInfo, memory sourceeffect.Memory) (result error) {
	release, err := memory.ReserveProgramState(ctx, (16<<10)+8*int64(len(location.Root)+len(location.Rel)))
	if err != nil {
		return err
	}
	defer release()
	directory, err := fseffect.OpenRead(location)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, directory.Close()) }()
	opened, err := directory.Stat()
	if err != nil {
		return err
	}
	if !opened.IsDir() || !os.SameFile(selected, opened) {
		return ErrSourceMutationDiverged
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	entries, err := directory.ReadDir(1)
	if len(entries) != 0 {
		return ErrSourceNotEmpty
	}
	if errors.Is(err, io.EOF) {
		return nil
	}
	return err
}
