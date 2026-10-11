package sourceledger

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/sourcebranch"
)

// A directory may already be gone when its event arrives. Select descendants
// from recorded paths rather than relying on the directory's current type.
func (s *Inventory) observedPathHeads(ctx context.Context, projectID string, roots []RootSpec, refs []PathRef) ([]db.SourceBranchHeads, error) {
	var heads []db.SourceBranchHeads
	var failures []error
	seen := make(map[string]struct{})
	for _, ref := range refs {
		if ref.Path == "" || !hasObservedRoot(roots, ref.RootID) {
			continue
		}
		selected, err := s.headsAtObservedPath(ctx, projectID, branchForObservedRoot(roots, ref.RootID), ref)
		if err != nil {
			failures = append(failures, fmt.Errorf("heads %s: %w", ref.Path, err))
			continue
		}
		for _, head := range selected {
			if _, duplicate := seen[head.FileID]; duplicate {
				continue
			}
			seen[head.FileID] = struct{}{}
			file, err := s.queries.GetSourceFile(ctx, head.FileID)
			if err != nil {
				failures = append(failures, fmt.Errorf("file %s: %w", head.Path, err))
				continue
			}
			if file.EntryKind == EntryKindDirectory {
				continue
			}
			heads = append(heads, head)
		}
	}
	return heads, errors.Join(failures...)
}

func (s *Inventory) headsAtObservedPath(ctx context.Context, projectID string, branch sourcebranch.ID, ref PathRef) ([]db.SourceBranchHeads, error) {
	head, err := s.queries.GetSourceBranchHeadByPath(ctx, db.GetSourceBranchHeadByPathParams{
		ProjectID: projectID, BranchID: branch.String(), RootID: ref.RootID, Path: ref.Path,
	})
	if err == nil {
		file, fileErr := s.queries.GetSourceFile(ctx, head.FileID)
		if fileErr != nil {
			return nil, fileErr
		}
		if file.EntryKind != EntryKindDirectory {
			return []db.SourceBranchHeads{head}, nil
		}
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	return s.queries.ListSourceBranchHeadsUnderPath(ctx, db.ListSourceBranchHeadsUnderPathParams{
		ProjectID: projectID, BranchID: branch.String(), RootID: ref.RootID, ParentPath: ref.Path,
	})
}

func hasObservedRoot(roots []RootSpec, id string) bool {
	for _, root := range roots {
		if root.ID == id {
			return true
		}
	}
	return false
}
func branchForObservedRoot(roots []RootSpec, id string) sourcebranch.ID {
	for _, root := range roots {
		if root.ID == id {
			return root.BranchID
		}
	}
	return sourcebranch.Trunk
}
func (s *Inventory) observedRootHeads(ctx context.Context, projectID string, roots []RootSpec) ([]db.SourceBranchHeads, error) {
	var result []db.SourceBranchHeads
	for _, root := range roots {
		heads, err := s.queries.ListSourceRootHeads(ctx, db.ListSourceRootHeadsParams{ProjectID: projectID, BranchID: root.BranchID.String(), RootID: root.ID})
		if err != nil {
			return nil, err
		}
		result = append(result, heads...)
	}
	return result, nil
}
