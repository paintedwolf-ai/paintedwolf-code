package sourceledger

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/pkg/api"
)

type walkFileState struct {
	rootID, path, state, contentSHA256  string
	presentationEffectID                string
	presentationOrdinal, throughOrdinal int64
	unpresentedAgentEffects             int64
}

// walkFileStates reads each file head on its root's selected branch.
func (s *Walk) walkFileStates(
	ctx context.Context,
	projectID string,
	rootBranches map[string]sourcebranch.ID,
	effects []Effect,
) (map[string]walkFileState, error) {
	ids := make([]string, 0, len(effects))
	seen := make(map[string]struct{}, len(effects))
	for _, effect := range effects {
		if _, ok := seen[effect.FileID]; ok {
			continue
		}
		seen[effect.FileID] = struct{}{}
		ids = append(ids, effect.FileID)
	}
	out := make(map[string]walkFileState, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	raw, err := json.Marshal(ids)
	if err != nil {
		return nil, err
	}
	roots, err := encodeRootBranches(rootBranches)
	if err != nil {
		return nil, err
	}
	// A root outside the request's mapping reads trunk.
	rows, err := s.sqlDB.QueryContext(ctx, `
		WITH requested(file_id) AS (
			SELECT CAST(value AS TEXT) FROM json_each(?)
		), scoped_roots(root_id, branch_id) AS (
			SELECT CAST(key AS TEXT), CAST(value AS TEXT)
			FROM json_each(CASE WHEN ? = '' THEN '{}' ELSE ? END)
		), latest(file_id, ordinal) AS (
			SELECT e.file_id, MAX(e.ordinal)
			FROM source_effects e
			JOIN source_operations o ON o.id = e.operation_id
			JOIN requested r ON r.file_id = e.file_id
			WHERE e.project_id = ? AND e.walk_visible = 1
			  AND o.branch_id = COALESCE((SELECT sr.branch_id FROM scoped_roots sr WHERE sr.root_id = e.root_id), '')
			GROUP BY e.file_id
		), pending(file_id, effect_count) AS (
			SELECT p.file_id, COUNT(*)
			FROM source_agent_presentations p
			JOIN requested r ON r.file_id = p.file_id
			WHERE p.project_id = ?
			GROUP BY p.file_id
		)
		SELECT r.file_id, COALESCE(h.root_id, ''), COALESCE(h.path, ''),
		       COALESCE(h.state, ''), COALESCE(h.content_sha256, ''),
		       COALESCE(e.id, ''), COALESCE(latest.ordinal, 0),
		       COALESCE(w.through_ordinal, 0), COALESCE(p.effect_count, 0)
		FROM requested r
		LEFT JOIN source_branch_heads h
		  ON h.project_id = ? AND h.file_id = r.file_id
		 AND h.branch_id = COALESCE((SELECT sr.branch_id FROM scoped_roots sr WHERE sr.root_id = h.root_id), '')
		LEFT JOIN latest ON latest.file_id = r.file_id
		LEFT JOIN source_effects e
		  ON e.project_id = ? AND e.file_id = latest.file_id AND e.ordinal = latest.ordinal
		LEFT JOIN source_presentation_watermarks w
		  ON w.project_id = ? AND w.file_id = r.file_id
		LEFT JOIN pending p ON p.file_id = r.file_id
	`, string(raw), roots, roots, projectID, projectID, projectID, projectID, projectID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var fileID string
		var state walkFileState
		if err := rows.Scan(&fileID, &state.rootID, &state.path, &state.state,
			&state.contentSHA256, &state.presentationEffectID, &state.presentationOrdinal,
			&state.throughOrdinal, &state.unpresentedAgentEffects); err != nil {
			return nil, err
		}
		out[fileID] = state
	}
	return out, rows.Err()
}

func (s *Walk) queryEffects(
	ctx context.Context,
	projectID string,
	baseline Baseline,
	limit int,
	beforeOrdinal int64,
) ([]Effect, error) {
	rootBranches, err := encodeRootBranches(baseline.RootBranches)
	if err != nil {
		return nil, err
	}
	switch baseline.Kind {
	case BaselineCommit:
		return nil, fmt.Errorf("commit comparisons are path queries, not recorded history")
	case BaselinePresentation:
		rows, err := s.queries.ListSourceEffectsForPresentation(ctx, db.ListSourceEffectsForPresentationParams{
			ProjectID: projectID, RootBranches: rootBranches, BeforeOrdinal: beforeOrdinal, PageLimit: int64(limit),
		})
		return effectsFromPresentation(rows), err
	case BaselineTurn:
		rows, err := s.queries.ListSourceEffectsForTurn(ctx, db.ListSourceEffectsForTurnParams{
			ProjectID: projectID, RootBranches: rootBranches, SessionID: baseline.SessionID, Turn: int64(baseline.Turn),
			BeforeOrdinal: beforeOrdinal, PageLimit: int64(limit),
		})
		return effectsFromTurn(rows), err
	case BaselineSession:
		if baseline.WithOutsideChanges {
			rows, err := s.queries.ListSourceEffectsForSessionWithOutside(ctx, db.ListSourceEffectsForSessionWithOutsideParams{
				ProjectID: projectID, RootBranches: rootBranches, SessionID: baseline.SessionID,
				BeforeOrdinal: beforeOrdinal, PageLimit: int64(limit),
			})
			return effectsFromSessionWithOutside(rows), err
		}
		rows, err := s.queries.ListSourceEffectsForSession(ctx, db.ListSourceEffectsForSessionParams{
			ProjectID: projectID, RootBranches: rootBranches, SessionID: baseline.SessionID,
			BeforeOrdinal: beforeOrdinal, PageLimit: int64(limit),
		})
		return effectsFromSession(rows), err
	case BaselinePin:
		ordinal, err := s.resolvePinOrdinal(ctx, projectID, baseline)
		if err != nil {
			return nil, err
		}
		rows, err := s.queries.ListSourceEffectsAfterOrdinal(ctx, db.ListSourceEffectsAfterOrdinalParams{
			ProjectID: projectID, RootBranches: rootBranches, Ordinal: ordinal,
			BeforeOrdinal: beforeOrdinal, PageLimit: int64(limit),
		})
		return effectsFromAfterOrdinal(rows), err
	default:
		rows, err := s.queries.ListSourceEffectsForProject(ctx, db.ListSourceEffectsForProjectParams{
			ProjectID: projectID, RootBranches: rootBranches, BeforeOrdinal: beforeOrdinal, PageLimit: int64(limit),
		})
		return effectsFromProject(rows), err
	}
}

// json_each treats JSON null as one row, so empty sets encode as [].
func jsonArray[T any](values []T) (string, error) {
	if len(values) == 0 {
		return "[]", nil
	}
	raw, err := json.Marshal(values)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

type effectFields struct {
	id, projectID, operationID, fileID, beforeVersionID, afterVersionID string
	rootID, path, fromRootID, fromPath, op, entryKind, createdTS        string
	branchID, origin, cause, actorLabel                                 string
	sessionID, jobID, toolCallID, toolName, batchID, captureQuality     string
	gitTransitionID, commandWindowID                                    string
	ordinal, turn                                                       int64
}

func effectFromFields(row effectFields) Effect {
	ts, _ := time.Parse(time.RFC3339Nano, row.createdTS)
	return Effect{
		ID: row.id, ProjectID: row.projectID, OperationID: row.operationID,
		FileID: row.fileID, BeforeVersionID: row.beforeVersionID,
		AfterVersionID: row.afterVersionID, RootID: row.rootID, Path: row.path,
		FromRootID: row.fromRootID, FromPath: row.fromPath,
		Op: api.SourceChangeOp(row.op), EntryKind: row.entryKind,
		BranchID: sourcebranch.ID(row.branchID),
		Origin:   api.SourceChangeOrigin(row.origin), Cause: row.cause, ActorLabel: row.actorLabel,
		SessionID: row.sessionID, JobID: row.jobID, Turn: int(row.turn),
		ToolCallID: row.toolCallID, ToolName: row.toolName, BatchID: row.batchID,
		GitTransitionID: row.gitTransitionID, CommandWindowID: row.commandWindowID,
		CaptureQuality: row.captureQuality, Ordinal: row.ordinal, TS: ts,
	}
}

func effectFieldsOf(
	id, projectID, operationID, fileID, beforeVersionID, afterVersionID,
	rootID, path, fromRootID, fromPath, op, entryKind string,
	ordinal int64,
	createdTS, branchID, origin, cause, actorLabel,
	sessionID, jobID string,
	turn int64,
	toolCallID, toolName, batchID, captureQuality, gitTransitionID, commandWindowID string,
) effectFields {
	return effectFields{id: id, projectID: projectID, operationID: operationID,
		fileID: fileID, beforeVersionID: beforeVersionID, afterVersionID: afterVersionID,
		rootID: rootID, path: path, fromRootID: fromRootID, fromPath: fromPath,
		op: op, entryKind: entryKind, ordinal: ordinal, createdTS: createdTS,
		branchID: branchID, origin: origin,
		cause: cause, actorLabel: actorLabel, sessionID: sessionID, jobID: jobID,
		turn: turn, toolCallID: toolCallID, toolName: toolName, batchID: batchID,
		captureQuality: captureQuality, gitTransitionID: gitTransitionID,
		commandWindowID: commandWindowID}
}

func effectOfProject(r db.ListSourceEffectsForProjectRow) Effect {
	return effectFromFields(effectFieldsOf(r.EffectID, r.ProjectID, r.OperationID, r.FileID,
		r.BeforeVersionID, r.AfterVersionID, r.RootID, r.Path, r.FromRootID, r.FromPath,
		r.Op, r.EntryKind, r.Ordinal, r.CreatedTs, r.BranchID,
		r.Origin, r.Cause, r.ActorLabel, r.SessionID, r.JobID, r.Turn,
		r.ToolCallID, r.ToolName, r.BatchID, r.CaptureQuality, r.GitTransitionID, r.CommandWindowID))
}

func effectsFromProject(rows []db.ListSourceEffectsForProjectRow) []Effect {
	out := make([]Effect, 0, len(rows))
	for _, row := range rows {
		out = append(out, effectOfProject(row))
	}
	return out
}
func effectsFromPresentation(rows []db.ListSourceEffectsForPresentationRow) []Effect {
	out := make([]Effect, 0, len(rows))
	for _, r := range rows {
		out = append(out, effectFromFields(effectFieldsOf(r.EffectID, r.ProjectID, r.OperationID, r.FileID, r.BeforeVersionID, r.AfterVersionID, r.RootID, r.Path, r.FromRootID, r.FromPath, r.Op, r.EntryKind, r.Ordinal, r.CreatedTs, r.BranchID, r.Origin, r.Cause, r.ActorLabel, r.SessionID, r.JobID, r.Turn, r.ToolCallID, r.ToolName, r.BatchID, r.CaptureQuality, r.GitTransitionID, r.CommandWindowID)))
	}
	return out
}
func effectsFromTurn(rows []db.ListSourceEffectsForTurnRow) []Effect {
	out := make([]Effect, 0, len(rows))
	for _, r := range rows {
		out = append(out, effectFromFields(effectFieldsOf(r.EffectID, r.ProjectID, r.OperationID, r.FileID, r.BeforeVersionID, r.AfterVersionID, r.RootID, r.Path, r.FromRootID, r.FromPath, r.Op, r.EntryKind, r.Ordinal, r.CreatedTs, r.BranchID, r.Origin, r.Cause, r.ActorLabel, r.SessionID, r.JobID, r.Turn, r.ToolCallID, r.ToolName, r.BatchID, r.CaptureQuality, r.GitTransitionID, r.CommandWindowID)))
	}
	return out
}
func effectsFromSession(rows []db.ListSourceEffectsForSessionRow) []Effect {
	out := make([]Effect, 0, len(rows))
	for _, r := range rows {
		out = append(out, effectFromFields(effectFieldsOf(r.EffectID, r.ProjectID, r.OperationID, r.FileID, r.BeforeVersionID, r.AfterVersionID, r.RootID, r.Path, r.FromRootID, r.FromPath, r.Op, r.EntryKind, r.Ordinal, r.CreatedTs, r.BranchID, r.Origin, r.Cause, r.ActorLabel, r.SessionID, r.JobID, r.Turn, r.ToolCallID, r.ToolName, r.BatchID, r.CaptureQuality, r.GitTransitionID, r.CommandWindowID)))
	}
	return out
}
func effectsFromSessionWithOutside(rows []db.ListSourceEffectsForSessionWithOutsideRow) []Effect {
	out := make([]Effect, 0, len(rows))
	for _, r := range rows {
		out = append(out, effectFromFields(effectFieldsOf(r.EffectID, r.ProjectID, r.OperationID, r.FileID, r.BeforeVersionID, r.AfterVersionID, r.RootID, r.Path, r.FromRootID, r.FromPath, r.Op, r.EntryKind, r.Ordinal, r.CreatedTs, r.BranchID, r.Origin, r.Cause, r.ActorLabel, r.SessionID, r.JobID, r.Turn, r.ToolCallID, r.ToolName, r.BatchID, r.CaptureQuality, r.GitTransitionID, r.CommandWindowID)))
	}
	return out
}
func effectsFromAfterOrdinal(rows []db.ListSourceEffectsAfterOrdinalRow) []Effect {
	out := make([]Effect, 0, len(rows))
	for _, r := range rows {
		out = append(out, effectFromFields(effectFieldsOf(r.EffectID, r.ProjectID, r.OperationID, r.FileID, r.BeforeVersionID, r.AfterVersionID, r.RootID, r.Path, r.FromRootID, r.FromPath, r.Op, r.EntryKind, r.Ordinal, r.CreatedTs, r.BranchID, r.Origin, r.Cause, r.ActorLabel, r.SessionID, r.JobID, r.Turn, r.ToolCallID, r.ToolName, r.BatchID, r.CaptureQuality, r.GitTransitionID, r.CommandWindowID)))
	}
	return out
}
