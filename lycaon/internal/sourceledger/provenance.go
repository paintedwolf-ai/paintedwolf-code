package sourceledger

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/sourcebranch"
)

func (s *History) SessionActivityFloor(ctx context.Context, projectID, sessionID string) (int64, bool, error) {
	if s == nil {
		return 0, false, fmt.Errorf("ledger not configured")
	}
	row, err := s.queries.EarliestTurnCheckpointForSession(ctx, db.EarliestTurnCheckpointForSessionParams{
		ProjectID: projectID, SessionID: sessionID,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return row.CreatedOrdinal, true, nil
}

// EffectsForBriefClass pages the newest of one brief class in
// (afterOrdinal, throughOrdinal]: the viewer's own command windows when
// duringCommand is set, otherwise every other actor. Worker branches and the
// viewer's own writes are in neither, so no class can fill another's page.
func (s *History) EffectsForBriefClass(
	ctx context.Context,
	projectID, viewerSessionID string,
	duringCommand bool,
	afterOrdinal, throughOrdinal int64,
	limit int,
) ([]Effect, error) {
	if s == nil {
		return nil, fmt.Errorf("ledger not configured")
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	class := int64(0)
	if duringCommand {
		class = 1
	}
	rows, err := s.queries.ListSourceEffectsForBriefClass(ctx, db.ListSourceEffectsForBriefClassParams{
		ProjectID: projectID, Ordinal: afterOrdinal, ThroughOrdinal: throughOrdinal,
		ViewerSessionID: viewerSessionID, DuringCommand: class, PageLimit: int64(limit),
	})
	if err != nil {
		return nil, err
	}
	out := make([]Effect, 0, len(rows))
	for _, r := range rows {
		out = append(out, effectFromFields(effectFieldsOf(r.EffectID, r.ProjectID, r.OperationID, r.FileID, r.BeforeVersionID, r.AfterVersionID, r.RootID, r.Path, r.FromRootID, r.FromPath, r.Op, r.EntryKind, r.Ordinal, r.CreatedTs, r.BranchID, r.Origin, r.Cause, r.ActorLabel, r.SessionID, r.JobID, r.Turn, r.ToolCallID, r.ToolName, r.BatchID, r.CaptureQuality, r.GitTransitionID, r.CommandWindowID)))
	}
	return out, nil
}

// EffectsBetweenOnBranch pages newest effects in (afterOrdinal, throughOrdinal]
// where each root admits only the branch given for it. Unlisted roots contribute
// nothing; a zero upper bound is open.
func (s *History) EffectsBetweenOnBranch(
	ctx context.Context,
	projectID string,
	rootBranches map[string]sourcebranch.ID,
	afterOrdinal, throughOrdinal int64,
	limit int,
) ([]Effect, error) {
	if s == nil {
		return nil, fmt.Errorf("ledger not configured")
	}
	if len(rootBranches) == 0 {
		return nil, nil
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	roots, err := encodeRootBranches(rootBranches)
	if err != nil {
		return nil, err
	}
	beforeOrdinal := int64(0)
	if throughOrdinal > 0 {
		beforeOrdinal = throughOrdinal + 1
	}
	rows, err := s.queries.ListSourceEffectsAfterOrdinalOnBranch(ctx, db.ListSourceEffectsAfterOrdinalOnBranchParams{
		ProjectID: projectID, RootBranches: roots, Ordinal: afterOrdinal,
		BeforeOrdinal: beforeOrdinal, PageLimit: int64(limit),
	})
	if err != nil {
		return nil, err
	}
	out := make([]Effect, 0, len(rows))
	for _, r := range rows {
		out = append(out, effectFromFields(effectFieldsOf(
			r.EffectID, r.ProjectID, r.OperationID, r.FileID, r.BeforeVersionID,
			r.AfterVersionID, r.RootID, r.Path, r.FromRootID, r.FromPath, r.Op,
			r.EntryKind, r.Ordinal, r.CreatedTs, r.BranchID,
			r.Origin, r.Cause, r.ActorLabel, r.SessionID, r.JobID, r.Turn,
			r.ToolCallID, r.ToolName, r.BatchID, r.CaptureQuality, r.GitTransitionID, r.CommandWindowID)))
	}
	if err := s.walk.hydrateEffectAuthors(ctx, projectID, out); err != nil {
		return nil, err
	}
	return out, nil
}

// OldestFileEffectAfterOnBranch returns the version bounds of the branch's first
// effect on the file past afterOrdinal; found=false means none was recorded.
func (s *History) OldestFileEffectAfterOnBranch(
	ctx context.Context,
	projectID string,
	branch sourcebranch.ID,
	fileID string,
	afterOrdinal int64,
) (FileEffectBounds, bool, error) {
	if s == nil {
		return FileEffectBounds{}, false, fmt.Errorf("ledger not configured")
	}
	row, err := s.queries.OldestSourceEffectForFileAfterOrdinalOnBranch(ctx, db.OldestSourceEffectForFileAfterOrdinalOnBranchParams{
		ProjectID: projectID, FileID: fileID, BranchID: branch.String(), Ordinal: afterOrdinal,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return FileEffectBounds{}, false, nil
	}
	if err != nil {
		return FileEffectBounds{}, false, err
	}
	return FileEffectBounds{
		BeforeVersionID: row.BeforeVersionID, AfterVersionID: row.AfterVersionID, Ordinal: row.Ordinal,
	}, true, nil
}

// OldestPathEffectAfterOnBranch returns the version bounds of the branch's first
// effect at a location past afterOrdinal, whichever file it belongs to. It finds
// what a path held when its current file has no predecessor of its own.
func (s *History) OldestPathEffectAfterOnBranch(
	ctx context.Context,
	projectID string,
	branch sourcebranch.ID,
	rootID, path string,
	afterOrdinal int64,
) (FileEffectBounds, bool, error) {
	if s == nil {
		return FileEffectBounds{}, false, fmt.Errorf("ledger not configured")
	}
	row, err := s.queries.OldestSourceEffectAtPathAfterOrdinalOnBranch(ctx, db.OldestSourceEffectAtPathAfterOrdinalOnBranchParams{
		ProjectID: projectID, BranchID: branch.String(), RootID: rootID, Path: path, Ordinal: afterOrdinal,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return FileEffectBounds{}, false, nil
	}
	if err != nil {
		return FileEffectBounds{}, false, err
	}
	before := row.BeforeVersionID
	if row.FromPath != "" && (row.FromRootID != rootID || row.FromPath != path) {
		// A move into the location says nothing about what it held before.
		before = ""
	}
	return FileEffectBounds{
		BeforeVersionID: before, AfterVersionID: row.AfterVersionID, Ordinal: row.Ordinal,
		FromRootID: row.FromRootID, FromPath: row.FromPath,
	}, true, nil
}

// FileEffectBounds is the version movement of one effect. BeforeVersionID is
// empty when the file did not exist before it.
type FileEffectBounds struct {
	BeforeVersionID, AfterVersionID string
	Ordinal                         int64
	// FromRootID and FromPath name a move's source. Only a lookup by path sets them.
	FromRootID, FromPath string
}

// FileEffectsResult is one newest-first page of a file's effects on one branch.
type FileEffectsResult struct {
	Effects           []Effect
	NextBeforeOrdinal int64
}

// afterOrdinal is exclusive; zero removes the lower bound.
// beforeOrdinal resumes an older page; zero starts at the newest effects.
func (s *History) QueryFileEffects(
	ctx context.Context,
	projectID string, branch sourcebranch.ID, fileID string,
	afterOrdinal, beforeOrdinal int64,
	limit int,
) (FileEffectsResult, error) {
	if s == nil {
		return FileEffectsResult{}, fmt.Errorf("ledger not configured")
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.queries.ListSourceEffectsForFilePage(ctx, db.ListSourceEffectsForFilePageParams{
		ProjectID: projectID, FileID: fileID, BranchID: branch.String(),
		AfterOrdinal: afterOrdinal, BeforeOrdinal: beforeOrdinal, PageLimit: int64(limit + 1),
	})
	if err != nil {
		return FileEffectsResult{}, err
	}
	out := FileEffectsResult{Effects: make([]Effect, 0, min(len(rows), limit))}
	for _, r := range rows {
		out.Effects = append(out.Effects, effectFromFields(effectFieldsOf(
			r.EffectID, r.ProjectID, r.OperationID, r.FileID, r.BeforeVersionID,
			r.AfterVersionID, r.RootID, r.Path, r.FromRootID, r.FromPath, r.Op,
			r.EntryKind, r.Ordinal, r.CreatedTs, r.BranchID,
			r.Origin, r.Cause, r.ActorLabel, r.SessionID, r.JobID, r.Turn,
			r.ToolCallID, r.ToolName, r.BatchID, r.CaptureQuality, r.GitTransitionID, r.CommandWindowID)))
	}
	if len(out.Effects) > limit {
		out.Effects = out.Effects[:limit]
		out.NextBeforeOrdinal = out.Effects[len(out.Effects)-1].Ordinal
	}
	if err := s.walk.hydrateEffectAuthors(ctx, projectID, out.Effects); err != nil {
		return FileEffectsResult{}, err
	}
	return out, nil
}

// LatestFileEffect returns the branch's newest effect; found=false means provenance is unknown.
func (s *History) LatestFileEffect(ctx context.Context, projectID string, branch sourcebranch.ID, fileID string) (Effect, bool, error) {
	res, err := s.QueryFileEffects(ctx, projectID, branch, fileID, 0, 0, 1)
	if err != nil || len(res.Effects) == 0 {
		return Effect{}, false, err
	}
	return res.Effects[0], true, nil
}

func checkpointFromColumns(
	id, projectID, kind, label, parentID, sessionID string,
	turn, createdOrdinal int64,
	createdTS string,
) Checkpoint {
	ts, _ := time.Parse(checkpointTimeLayout, createdTS)
	return Checkpoint{
		ID: id, ProjectID: projectID, Kind: kind,
		Label: label, ParentID: parentID, SessionID: sessionID,
		Turn: int(turn), CreatedOrdinal: createdOrdinal, CreatedTS: ts,
	}
}
