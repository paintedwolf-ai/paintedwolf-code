package sourceledger

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sourceblob"
	"github.com/lycaon/lycaon/internal/sourcesnapshot"
	"github.com/lycaon/lycaon/pkg/api"
)

func rootIDsByPath(roots []RootSpec) map[string]string {
	out := make(map[string]string, len(roots))
	for _, root := range roots {
		out[cleanRootPath(root.Path)] = root.ID
	}
	return out
}

func rootPathsByID(rootIDs map[string]string) map[string]string {
	out := make(map[string]string, len(rootIDs))
	for path, id := range rootIDs {
		out[id] = path
	}
	return out
}

// rootRefsOf projects this pass's roots for callers that address a tree.
func rootRefsOf(roots []RootSpec) []projectroot.RootRef {
	refs := make([]projectroot.RootRef, 0, len(roots))
	for _, root := range roots {
		refs = append(refs, projectroot.RootRef{ID: root.ID, Path: root.Path})
	}
	return refs
}

// reconcileOutcome is what one pass found and what it wrote.
type reconcileOutcome struct {
	files    int
	recorded int
}

// The file changed after identification; a later pass can read it.
var errVersionMovedOn = errors.New("source file moved on since the generation identified it")

// Reconcile tracked heads by manifest lookup. An open command window also
// admits untracked changes against its starting inventory.
func (s *Inventory) reconcileSnapshot(
	ctx context.Context,
	projectID string,
	snapshot sourcesnapshot.Snapshot,
	roots []RootSpec,
	transitionByRoot map[string]string,
	window *openCommandWindow,
) (reconcileOutcome, error) {
	out := reconcileOutcome{files: snapshot.FileCount}
	initialized, err := s.hasTrackingCheckpoint(ctx, projectID)
	if err != nil {
		return out, err
	}
	if !initialized {
		return out, s.seedTrackingBoundary(ctx, projectID)
	}
	rootIDs := rootIDsByPath(roots)
	heads, err := s.observedRootHeads(ctx, projectID, roots)
	if err != nil {
		return out, err
	}
	// One pass is one observation batch.
	batchID := newID()
	rootPaths := rootPathsByID(rootIDs)
	live := make(map[string]struct{}, len(heads))
	for _, head := range heads {
		if head.State == "absent" {
			continue
		}
		key := entryKey(head.RootID, head.Path)
		live[key] = struct{}{}
		rootPath, known := rootPaths[head.RootID]
		if !known {
			continue
		}
		entry, found, err := s.snapshots.Lookup(ctx, snapshot.ID, rootPath, head.Path)
		if err != nil {
			return out, err
		}
		if found {
			same, err := s.headHoldsEntry(ctx, head, entry)
			if err != nil {
				return out, err
			}
			if same {
				continue
			}
		}
		// The manifest can predate a write recorded during its capture, so
		// omission or disagreement is confirmed against the live file, read
		// after the head it is compared with.
		entry, found, err = s.snapshots.Identify(ctx, rootPath, head.Path)
		if err != nil {
			return out, err
		}
		var observed *observedFile
		if found {
			same, err := s.headHoldsEntry(ctx, head, entry)
			if err != nil {
				return out, err
			}
			if same {
				continue
			}
			observed, err = s.observedVersion(ctx, head.RootID, entry)
			if errors.Is(err, errVersionMovedOn) {
				continue
			}
			if err != nil {
				return out, err
			}
		}
		cause := observationCause{batchID: batchID, gitTransitionID: transitionByRoot[head.RootID], window: window}
		landed, err := s.recordObservation(ctx, projectID, head, observed, newID(), cause)
		if err != nil {
			return out, err
		}
		if landed {
			out.recorded++
		}
	}
	if window == nil {
		return out, nil
	}
	window.mu.Lock()
	window.admissionMode = string(snapshot.AdmissionMode)
	window.mu.Unlock()
	admitted, err := s.windowAdmissions(ctx, projectID, batchID, window, snapshot, live, rootIDs, transitionByRoot)
	if err != nil {
		return out, err
	}
	recorded, err := s.recordWindowAdmissions(ctx, admitted)
	out.recorded += recorded
	return out, err
}

// Compare raw digests, then retained object IDs, then file bytes.
func (s *Inventory) headHoldsEntry(ctx context.Context, head db.SourceBranchHeads, entry sourcesnapshot.Entry) (bool, error) {
	if head.State != "content" || head.ContentSha256 == "" {
		return false, nil
	}
	if entry.SHA256 != "" {
		return entry.SHA256 == head.ContentSha256, nil
	}
	if entry.GitOID != "" {
		object, err := s.queries.GetSourceBlobObject(ctx, head.ContentSha256)
		if err == nil {
			return object.GitOidSha1 == entry.GitOID, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return false, err
		}
	}
	digest, err := s.snapshots.Digest(ctx, entry)
	if errors.Is(err, sourcesnapshot.ErrContentUnavailable) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return digest == head.ContentSha256, nil
}

// observedVersion is what a pass learned about one admitted file: its
// digest and, when small enough and still held somewhere, its bytes.
func (s *Inventory) observedVersion(ctx context.Context, rootID string, entry sourcesnapshot.Entry) (*observedFile, error) {
	sha, raw, err := s.versionFacts(ctx, entry)
	if err != nil {
		return nil, err
	}
	if sha == "" {
		return nil, errVersionMovedOn
	}
	return &observedFile{rootID: rootID, path: entry.Path, sha: sha, bytes: raw, size: entry.Size}, nil
}

// Resolve the digest and bounded content from available snapshot bytes.
func (s *Inventory) versionFacts(ctx context.Context, entry sourcesnapshot.Entry) (string, []byte, error) {
	sha := entry.SHA256
	var raw []byte
	if entry.Size <= MaxRevisionContentBytes {
		bytes, err := s.snapshots.Bytes(ctx, entry)
		switch {
		case err == nil:
			raw = bytes
			if sha == "" {
				sha = sourceblob.ContentSHA(raw)
			}
		case !errors.Is(err, sourcesnapshot.ErrContentUnavailable):
			return "", nil, err
		}
	}
	if sha == "" {
		digest, err := s.snapshots.Digest(ctx, entry)
		switch {
		case err == nil:
			sha = digest
		case !errors.Is(err, sourcesnapshot.ErrContentUnavailable):
			return "", nil, err
		}
	}
	return sha, raw, nil
}

// Admit untracked changes against the window's start, reading only changed manifest buckets.
func (s *Inventory) windowAdmissions(
	ctx context.Context,
	projectID, batchID string,
	window *openCommandWindow,
	snapshot sourcesnapshot.Snapshot,
	live map[string]struct{},
	rootIDs map[string]string,
	transitionByRoot map[string]string,
) ([]RecordInput, error) {
	startID, ok := window.startSnapshot(ctx, s.snapshots)
	if !ok {
		return nil, nil
	}
	var inputs []RecordInput
	now := time.Now().UTC()
	admit := func(rootID, path string, op api.SourceChangeOp, before, after *sourcesnapshot.Entry) error {
		in := observationCause{batchID: batchID, gitTransitionID: transitionByRoot[rootID], window: window}.apply(RecordInput{
			ProjectID: projectID, BranchID: branchForObservedRoot(window.roots, rootID),
			RootID: rootID, Path: path, Op: op, EntryKind: EntryKindFile, TS: now,
		})
		if before != nil {
			sha, raw, err := s.versionFacts(ctx, *before)
			if err != nil {
				return err
			}
			in.BeforeSHA256, in.BeforeSize, in.Before = sha, before.Size, raw
		}
		if after != nil {
			sha, raw, err := s.versionFacts(ctx, *after)
			if err != nil {
				return err
			}
			in.AfterSHA256, in.AfterSize, in.After = sha, after.Size, raw
		}
		inputs = append(inputs, in)
		return nil
	}
	err := s.snapshots.Diff(ctx, startID, snapshot.ID, func(change sourcesnapshot.Change) error {
		rootID, known := rootIDs[change.RootPath()]
		if !known {
			return nil
		}
		if _, tracked := live[entryKey(rootID, change.Path())]; tracked {
			return nil
		}
		switch {
		case change.Before == nil:
			return admit(rootID, change.Path(), api.SourceChangeOpCreate, nil, change.After)
		case change.After == nil:
			return admit(rootID, change.Path(), api.SourceChangeOpDelete, change.Before, nil)
		default:
			return admit(rootID, change.Path(), api.SourceChangeOpWrite, change.Before, change.After)
		}
	})
	return inputs, err
}

// seedTrackingBoundary records when sparse tracking begins.
func (s *Inventory) seedTrackingBoundary(ctx context.Context, projectID string) error {
	s.recordMu.Lock()
	defer s.recordMu.Unlock()
	if initialized, err := s.hasTrackingCheckpoint(ctx, projectID); err != nil || initialized {
		return err
	}
	tx, err := s.sqlDB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.queries.WithTx(tx)
	ordinal, err := q.LatestSourceOrdinal(ctx, projectID)
	if err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	parentID := ""
	if parent, parentErr := q.LatestStructuralSourceCheckpoint(ctx, projectID); parentErr == nil {
		parentID = parent.ID
	} else if !errors.Is(parentErr, sql.ErrNoRows) {
		return parentErr
	}
	if err := q.InsertSourceCheckpoint(ctx, db.InsertSourceCheckpointParams{
		ID: newID(), ProjectID: projectID, Kind: CheckpointTracking,
		Label: "Tracking started", ParentID: parentID,
		CreatedOrdinal: ordinal, CreatedTs: now,
	}); err != nil {
		return err
	}
	return tx.Commit()
}
