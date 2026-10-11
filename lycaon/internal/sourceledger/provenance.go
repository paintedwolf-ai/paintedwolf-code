package sourceledger

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/lycaon/lycaon/internal/db"
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

// Page newest effects in (afterOrdinal, throughOrdinal]; a zero upper bound is open.
func (s *History) EffectsBetween(
	ctx context.Context,
	projectID string,
	afterOrdinal, throughOrdinal int64,
	limit int,
) ([]Effect, error) {
	if s == nil {
		return nil, fmt.Errorf("ledger not configured")
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	beforeOrdinal := int64(0)
	if throughOrdinal > 0 {
		beforeOrdinal = throughOrdinal + 1
	}
	rows, err := s.queries.ListSourceEffectsAfterOrdinal(ctx, db.ListSourceEffectsAfterOrdinalParams{
		ProjectID: projectID, Ordinal: afterOrdinal,
		BeforeOrdinal: beforeOrdinal, PageLimit: int64(limit),
	})
	return effectsFromAfterOrdinal(rows), err
}

// FileEffectsResult is one newest-first page of a file's recorded effects.
type FileEffectsResult struct {
	Effects           []Effect
	NextBeforeOrdinal int64
}

// afterOrdinal is exclusive; zero removes the lower bound.
// beforeOrdinal resumes an older page; zero starts at the newest effects.
func (s *History) QueryFileEffects(
	ctx context.Context,
	projectID, fileID string,
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
		ProjectID: projectID, FileID: fileID,
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

// LatestFileEffect returns the newest effect; found=false means provenance is unknown.
func (s *History) LatestFileEffect(ctx context.Context, projectID, fileID string) (Effect, bool, error) {
	res, err := s.QueryFileEffects(ctx, projectID, fileID, 0, 0, 1)
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
