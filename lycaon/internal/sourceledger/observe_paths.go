package sourceledger

import (
	"context"
	"errors"
	"fmt"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/sourcefeed"
)

// PathRef addresses one root-relative file or subtree.
type PathRef struct {
	RootID string
	Path   string
}

// pathDrift is one tracked head whose file no longer holds its bytes.
type pathDrift struct {
	head     db.SourceBranchHeads
	observed *observedFile
	rootID   string
}

// ObservePaths records tracked-path drift in one transaction, after observing
// ref movements. It excludes untracked files and avoids a tree walk.
func (s *Store) ObservePaths(ctx context.Context, projectID string, roots []RootSpec, refs []PathRef) (int, error) {
	if s == nil || s.sqlDB == nil || len(refs) == 0 {
		return 0, nil
	}
	release, err := s.lockObservations(ctx, projectID, roots)
	if err != nil {
		return 0, err
	}
	defer release()
	transitionByRoot, gitErr := s.observeGitState(ctx, projectID, roots)
	recorded, err := s.observePaths(ctx, projectID, roots, refs, transitionByRoot, nil)
	return recorded, errors.Join(gitErr, err)
}

func (s *Store) observePaths(ctx context.Context, projectID string, roots []RootSpec, refs []PathRef, transitionByRoot map[string]string, actor *Contributor) (int, error) {
	if len(refs) == 0 {
		return 0, nil
	}
	rootPaths := make(map[string]string, len(roots))
	for _, root := range roots {
		rootPaths[root.ID] = cleanRootPath(root.Path)
	}
	var failures []error
	heads, err := s.observedPathHeads(ctx, projectID, roots, refs)
	if err != nil {
		failures = append(failures, err)
	}
	drifts := make([]pathDrift, 0, len(heads))
	for _, head := range heads {
		rootPath := rootPaths[head.RootID]
		entry, present, err := s.snapshots.Identify(ctx, rootPath, head.Path)
		if err != nil {
			failures = append(failures, fmt.Errorf("identify %s: %w", head.Path, err))
			continue
		}
		var observed *observedFile
		if present {
			same, err := s.headHoldsEntry(ctx, head, entry)
			if err != nil {
				failures = append(failures, fmt.Errorf("compare %s: %w", head.Path, err))
				continue
			}
			if same {
				continue
			}
			observed, err = s.observedVersion(ctx, head.RootID, entry)
			if errors.Is(err, errVersionMovedOn) {
				continue
			}
			if err != nil {
				failures = append(failures, fmt.Errorf("read %s: %w", head.Path, err))
				continue
			}
		}
		drifts = append(drifts, pathDrift{head: head, observed: observed, rootID: head.RootID})
	}
	recorded, err := s.recordPathDrift(ctx, projectID, roots, drifts, transitionByRoot, actor)
	if err != nil {
		failures = append(failures, err)
	}
	// History moved without a path batch of its own; projections re-read.
	if recorded > 0 {
		if err := sourcefeed.EmitProjectSignal(ctx, projectID, rootRefsOf(roots)); err != nil {
			failures = append(failures, fmt.Errorf("signal: %w", err))
		}
	}
	return recorded, errors.Join(failures...)
}

// recordPathDrift lands one batch of observations in one transaction.
func (s *Store) recordPathDrift(ctx context.Context, projectID string, roots []RootSpec, drifts []pathDrift, transitionByRoot map[string]string, actor *Contributor) (int, error) {
	if len(drifts) == 0 {
		return 0, nil
	}
	cause := observationCause{actor: actor, batchID: newID(), window: s.attributionWindow(projectID, roots)}
	s.recordMu.Lock()
	defer s.recordMu.Unlock()
	tx, err := s.sqlDB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	scope, err := s.mutationObservationScope(ctx, tx, projectID)
	if err != nil {
		return 0, err
	}
	recorded := 0
	for _, drift := range drifts {
		cause.gitTransitionID = transitionByRoot[drift.rootID]
		landed, err := s.recordObservationTx(ctx, tx, scope, projectID, drift.head, drift.observed, newID(), cause)
		if err != nil {
			return 0, fmt.Errorf("record %s: %w", drift.head.Path, err)
		}
		if landed {
			recorded++
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return recorded, nil
}
