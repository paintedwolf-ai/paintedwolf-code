package sourceledger

import (
	"context"
	"database/sql"
	"errors"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/sourcebranch"
)

type ReviewHistory struct {
	FileID                  string
	Effects                 []Effect
	Commands                []CommandWindow
	Truncated               bool
	PresentationEffectID    string
	PresentationOrdinal     int64
	ChangedSincePresented   bool
	UnpresentedAgentEffects int64
}

// ReviewPathHistory enriches a current path without admitting it to history.
// The latest identity at a deleted path remains addressable until recreation.
func (s *History) ReviewPathHistory(ctx context.Context, projectID string, branch sourcebranch.ID, rootID, path string, absent bool) (ReviewHistory, error) {
	var fileID, state string
	err := s.sqlDB.QueryRowContext(ctx, `SELECT file_id, state FROM source_branch_heads
		WHERE project_id = ? AND branch_id = ? AND root_id = ? AND path = ?
		ORDER BY ordinal DESC LIMIT 1`, projectID, branch, rootID, path).Scan(&fileID, &state)
	if errors.Is(err, sql.ErrNoRows) {
		return ReviewHistory{}, nil
	}
	if err != nil {
		return ReviewHistory{}, err
	}
	if state == "absent" && !absent {
		return ReviewHistory{}, nil
	}
	rows, err := s.queries.ListSourceEffectsForReviewFile(ctx, db.ListSourceEffectsForReviewFileParams{
		ProjectID: projectID, FileID: fileID, Limit: 101,
	})
	if err != nil {
		return ReviewHistory{}, err
	}
	more := len(rows) > 100
	if more {
		rows = rows[:100]
	}
	var effects []Effect
	for _, r := range rows {
		effects = append(effects, effectFromFields(effectFieldsOf(r.EffectID, r.ProjectID, r.OperationID, r.FileID,
			r.BeforeVersionID, r.AfterVersionID, r.RootID, r.Path, r.FromRootID, r.FromPath,
			r.Op, r.EntryKind, r.Ordinal, r.CreatedTs, r.BranchID,
			r.Origin, r.Cause, r.ActorLabel, r.SessionID, r.JobID, r.Turn,
			r.ToolCallID, r.ToolName, r.BatchID, r.CaptureQuality, r.GitTransitionID, r.CommandWindowID)))
	}
	if err := s.walk.hydrateEffectAuthors(ctx, projectID, effects); err != nil {
		return ReviewHistory{}, err
	}
	commands, err := s.walk.walkCommandWindows(ctx, effects)
	if err != nil {
		return ReviewHistory{}, err
	}
	states, err := s.walk.walkFileStates(ctx, projectID, map[string]sourcebranch.ID{rootID: branch}, []Effect{{FileID: fileID}})
	if err != nil {
		return ReviewHistory{}, err
	}
	presentation := states[fileID]
	return ReviewHistory{FileID: fileID, Effects: effects, Commands: commands, Truncated: more,
		PresentationEffectID: presentation.presentationEffectID, PresentationOrdinal: presentation.presentationOrdinal,
		ChangedSincePresented:   presentation.presentationOrdinal > presentation.throughOrdinal,
		UnpresentedAgentEffects: presentation.unpresentedAgentEffects}, nil
}
