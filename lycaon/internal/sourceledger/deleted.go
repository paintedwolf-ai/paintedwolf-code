package sourceledger

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
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

func (s *History) ResolveDeletedPath(ctx context.Context, projectID string, branch sourcebranch.ID, rootID, path string) (DeletedPath, error) {
	_, liveErr := s.queries.GetSourceBranchHeadByPath(ctx, db.GetSourceBranchHeadByPathParams{ProjectID: projectID, BranchID: branch.String(), RootID: rootID, Path: path})
	if liveErr == nil {
		return DeletedPath{}, ErrHistoryNotFound
	}
	if !errors.Is(liveErr, sql.ErrNoRows) {
		return DeletedPath{}, liveErr
	}
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

// DeletedPathContent reads retained content from before the entry became absent.
func (s *History) DeletedPathContent(ctx context.Context, projectID string, deleted DeletedPath) (ComparisonSide, error) {
	version, err := s.queries.GetSourceVersion(ctx, deleted.VersionID)
	if errors.Is(err, sql.ErrNoRows) {
		return ComparisonSide{}, ErrHistoryNotFound
	}
	if err != nil {
		return ComparisonSide{}, err
	}
	if version.ProjectID != projectID || version.FileID != deleted.FileID {
		return ComparisonSide{}, ErrHistoryNotFound
	}
	if version.State != "absent" {
		// A parent-directory deletion retains the child's own content version.
		return s.comparisons.comparisonSide(ctx, projectID, version.ID)
	}
	if version.ParentVersionID == "" {
		return ComparisonSide{State: "unresolved", Availability: ContentNotCaptured, Reason: "content_not_captured"}, nil
	}
	return s.comparisons.comparisonSide(ctx, projectID, version.ParentVersionID)
}

type ScopedDeletedPath struct{ RootID, Path string }
type DeletedPathPage struct {
	Paths             []ScopedDeletedPath
	NextBeforeOrdinal int64
}

// DeletedPaths reads scope membership and current absence without loading source
// bytes, command windows, turns, presentations, or Git object identities.
func (s *History) DeletedPaths(ctx context.Context, projectID string, baseline Baseline, limit int, beforeOrdinal int64) (DeletedPathPage, error) {
	if s == nil {
		return DeletedPathPage{}, fmt.Errorf("ledger not configured")
	}
	if limit < 1 || limit > 200 || beforeOrdinal < 0 {
		return DeletedPathPage{}, fmt.Errorf("invalid deleted path page")
	}
	effects, err := s.walk.queryEffects(ctx, projectID, baseline, limit+1, beforeOrdinal)
	if err != nil {
		return DeletedPathPage{}, err
	}
	out := DeletedPathPage{Paths: []ScopedDeletedPath{}}
	if len(effects) > limit {
		effects = effects[:limit]
		out.NextBeforeOrdinal = effects[len(effects)-1].Ordinal
	}
	if baseline.WithoutUserEdits {
		if err := s.walk.hydrateEffectAuthors(ctx, projectID, effects); err != nil {
			return out, err
		}
		effects = slices.DeleteFunc(effects, func(effect Effect) bool { return effect.onlyUserContributions() })
	}
	ids := make([]string, 0, len(effects))
	for _, effect := range effects {
		ids = append(ids, effect.FileID)
	}
	requested, err := jsonArray(ids)
	if err != nil {
		return out, err
	}
	roots, err := encodeRootBranches(baseline.RootBranches)
	if err != nil {
		return out, err
	}
	rows, err := s.sqlDB.QueryContext(ctx, `
 WITH requested(file_id) AS (SELECT CAST(value AS TEXT) FROM json_each(?)),
 scope_input(roots) AS (VALUES(?)),
 scoped_roots AS (
  SELECT CAST(r.key AS TEXT) AS root_id, CAST(r.value AS TEXT) AS branch_id
  FROM scope_input,json_each(CASE WHEN roots='' THEN '{}' ELSE roots END) r
 )
 SELECT DISTINCT h.root_id,h.path,h.branch_id,h.file_id FROM source_branch_heads h
 JOIN requested r ON r.file_id=h.file_id
 WHERE h.project_id=? AND h.state='absent'
 AND EXISTS(SELECT 1 FROM source_files f WHERE f.id=h.file_id AND f.entry_kind='file')
 AND (((SELECT roots FROM scope_input)='' AND (h.branch_id='' OR substr(h.branch_id,1,9)='worktree:'))
 OR EXISTS(SELECT 1 FROM scoped_roots s WHERE s.root_id=h.root_id AND s.branch_id=h.branch_id))
 ORDER BY h.root_id,h.path`, requested, roots, projectID)
	if err != nil {
		return out, err
	}
	defer func() { _ = rows.Close() }()
	type candidate struct{ root, path, branch, file string }
	var candidates []candidate
	for rows.Next() {
		var entry candidate
		if err := rows.Scan(&entry.root, &entry.path, &entry.branch, &entry.file); err != nil {
			return out, err
		}
		candidates = append(candidates, entry)
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	if err := rows.Close(); err != nil {
		return out, err
	}
	seen := map[ScopedDeletedPath]bool{}
	for _, entry := range candidates {
		deleted, err := s.ResolveDeletedPath(ctx, projectID, sourcebranch.ID(entry.branch), entry.root, entry.path)
		if errors.Is(err, ErrHistoryNotFound) {
			continue
		}
		if err != nil {
			return out, err
		}
		address := ScopedDeletedPath{RootID: entry.root, Path: entry.path}
		if deleted.FileID == entry.file && !seen[address] {
			out.Paths = append(out.Paths, address)
			seen[address] = true
		}
	}
	return out, nil
}
