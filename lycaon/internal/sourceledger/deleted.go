package sourceledger

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/sourcebranch"
)

// DeletedPath identifies the deletion of the latest working-file occupant at an address.
type DeletedPath struct {
	FileID    string
	VersionID string
	DeletedTS time.Time
}

func (s *Store) ResolveDeletedPath(ctx context.Context, projectID string, branch sourcebranch.ID, rootID, path string) (DeletedPath, error) {
	head, err := s.queries.GetDeletedSourcePathHead(ctx, db.GetDeletedSourcePathHeadParams{
		ProjectID: projectID, BranchID: branch.String(), RootID: rootID, Path: path,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return DeletedPath{}, ErrHistoryNotFound
	}
	if err != nil {
		return DeletedPath{}, err
	}
	if head.EntryKind != EntryKindFile {
		return DeletedPath{}, ErrHistoryNotFound
	}
	timestamp, err := db.ParseTime(head.ObservedTs)
	return DeletedPath{FileID: head.FileID, VersionID: head.VersionID, DeletedTS: timestamp}, err
}

// DeletedPathContent reads the parent version of the deletion on this branch.
func (s *Store) DeletedPathContent(ctx context.Context, projectID string, deleted DeletedPath) (ComparisonSide, error) {
	version, err := s.queries.GetSourceVersion(ctx, deleted.VersionID)
	if errors.Is(err, sql.ErrNoRows) {
		return ComparisonSide{}, ErrHistoryNotFound
	}
	if err != nil {
		return ComparisonSide{}, err
	}
	if version.ProjectID != projectID || version.FileID != deleted.FileID || version.State != "absent" {
		return ComparisonSide{}, ErrHistoryNotFound
	}
	if version.ParentVersionID == "" {
		return ComparisonSide{State: "unresolved", Availability: ContentNotCaptured, Reason: "content_not_captured"}, nil
	}
	return s.comparisonSide(ctx, projectID, version.ParentVersionID)
}
