package hitl

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/authzledger"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/eventoutbox"
	"github.com/lycaon/lycaon/pkg/api"
)

// SQLStore persists checkpoints in SQLite.
type SQLStore struct {
	db      db.Handle
	queries *db.Queries
	outbox  *eventoutbox.Outbox
}

func (s *SQLStore) SetEventOutbox(outbox *eventoutbox.Outbox) {
	if s != nil {
		s.outbox = outbox
	}
}

// NewSQLStore creates a checkpoint store backed by SQLite.
func NewSQLStore(database db.Handle) *SQLStore {
	return &SQLStore{db: database, queries: db.New(database)}
}

// SessionProjectID derives checkpoint ownership from the persisted session.
func (s *SQLStore) SessionProjectID(ctx context.Context, sessionID string) (string, error) {
	return s.queries.GetSessionProjectID(ctx, sessionID)
}

// Insert commits a checkpoint and its opening event together.
func (s *SQLStore) Insert(ctx context.Context, row StoredCheckpoint) error {
	params, err := checkpointInsertParams(row)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := s.queries.WithTx(tx).InsertCheckpoint(ctx, params); err != nil {
		return fmt.Errorf("insert checkpoint: %w", err)
	}
	if err := s.enqueueCheckpointTx(ctx, tx, row); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.outbox.Notify()
	return nil
}

func checkpointInsertParams(row StoredCheckpoint) (db.InsertCheckpointParams, error) {
	argsJSON, err := db.MarshalJSON(row.Args)
	if err != nil {
		return db.InsertCheckpointParams{}, fmt.Errorf("marshal args: %w", err)
	}
	filesJSON, err := db.MarshalJSON(row.Files)
	if err != nil {
		return db.InsertCheckpointParams{}, fmt.Errorf("marshal files: %w", err)
	}
	payloadJSON, err := db.MarshalJSON(row.Payload)
	if err != nil {
		return db.InsertCheckpointParams{}, fmt.Errorf("marshal payload: %w", err)
	}
	resultJSON, err := db.MarshalJSON(row.Result)
	if err != nil {
		return db.InsertCheckpointParams{}, fmt.Errorf("marshal result: %w", err)
	}
	return db.InsertCheckpointParams{
		ID:                 row.ID,
		SessionID:          row.SessionID,
		ProjectID:          row.ProjectID,
		ProjectDir:         row.ProjectDir,
		Kind:               string(row.Kind),
		Status:             string(row.Status),
		Type:               string(row.Type),
		Title:              row.Title,
		Description:        row.Description,
		ToolName:           row.ToolName,
		Path:               row.Path,
		ArgsJson:           argsJSON,
		FilesJson:          filesJSON,
		PayloadJson:        payloadJSON,
		ResultJson:         resultJSON,
		CreatedAt:          db.FormatTime(row.CreatedAt),
		ResolvedAt:         db.NullTimePtr(row.ResolvedAt),
		ResolvedBy:         db.NullString(resolutionBy(row.Resolution)),
		ResolvedByPersonID: db.NullString(resolutionPersonID(row.Resolution)),
	}, nil
}

// Get loads a checkpoint by id.
func (s *SQLStore) Get(ctx context.Context, checkpointID string) (*StoredCheckpoint, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	row, err := s.queries.GetCheckpoint(ctx, checkpointID)
	if db.IsNoRows(err) {
		return nil, ErrCheckpointNotFound
	}
	if err != nil {
		return nil, err
	}
	return checkpointFromRow(row)
}

// resolveCheckpoint commits a checkpoint, its transcript stamp, and the ledger
// seal in one transaction: no outcome resolves that the ledger does not record.
func (s *SQLStore) resolveCheckpoint(ctx context.Context, row StoredCheckpoint, status DecisionStatus, result *DecisionResult, contentResult *ContentApplyResolve, resolvedAt time.Time, resolution Resolution, seal func(*sql.Tx, StoredCheckpoint) error) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	resultPayload := map[string]any{}
	if result != nil {
		resultPayload["tool"] = result
	}
	if contentResult != nil {
		resultPayload["content_apply"] = contentResult
	}
	resultJSON, err := db.MarshalJSON(resultPayload)
	if err != nil {
		return false, fmt.Errorf("marshal result: %w", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	qtx := s.queries.WithTx(tx)
	rows, err := qtx.ResolveCheckpointForSession(ctx, db.ResolveCheckpointForSessionParams{
		NewStatus:          string(status),
		ResultJson:         resultJSON,
		ResolvedAt:         db.NullString(db.FormatTime(resolvedAt)),
		ResolvedBy:         db.NullString(resolution.By),
		ResolvedByPersonID: db.NullString(resolution.PersonID),
		ID:                 row.ID,
		SessionID:          row.SessionID,
		ExpectedStatus:     string(DecisionStatusPending),
	})
	if err != nil {
		return false, fmt.Errorf("update checkpoint: %w", err)
	}
	if rows != 1 {
		return false, ErrCheckpointNotFound
	}
	committed := row
	committed.Status = status
	committed.Result = result
	committed.ContentResult = contentResult
	committed.ResolvedAt = &resolvedAt
	committed.Resolution = &resolution
	if err := stageCheckpointDecisionTx(ctx, tx, committed, status); err != nil {
		return false, err
	}
	applied, err := patchCheckpointDecisionTx(ctx, tx, committed, status)
	if err != nil {
		return false, err
	}
	if applied {
		if err := deleteCheckpointDecisionStampTx(ctx, tx, committed); err != nil {
			return false, err
		}
	}
	if seal != nil {
		if err := seal(tx, committed); err != nil {
			return false, err
		}
	}
	viaOutbox := s.outbox != nil
	if err := s.enqueueCheckpointTx(ctx, tx, committed); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	if viaOutbox {
		s.outbox.Notify()
	}
	return viaOutbox, nil
}

// ListBySession returns checkpoints for a session filtered by status and optional kind.
func (s *SQLStore) ListBySession(ctx context.Context, sessionID string, status DecisionStatus, kind *api.CheckpointKind) ([]StoredCheckpoint, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	params := db.ListSessionCheckpointsParams{SessionID: sessionID}
	if status != "" {
		params.Status = string(status)
	}
	if kind != nil {
		params.Kind = string(*kind)
	}
	rows, err := s.queries.ListSessionCheckpoints(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("list checkpoints: %w", err)
	}
	out := make([]StoredCheckpoint, 0, len(rows))
	for _, r := range rows {
		row, err := checkpointFromRow(r)
		if err != nil {
			return nil, err
		}
		out = append(out, *row)
	}
	return out, nil
}

// ListPending returns pending checkpoints across sessions.
func (s *SQLStore) ListPending(ctx context.Context) ([]StoredCheckpoint, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	rows, err := s.queries.ListPendingCheckpoints(ctx)
	if err != nil {
		return nil, fmt.Errorf("list pending checkpoints: %w", err)
	}
	out := make([]StoredCheckpoint, 0, len(rows))
	for _, stored := range rows {
		row, err := checkpointFromRow(stored)
		if err != nil {
			return nil, err
		}
		out = append(out, *row)
	}
	return out, nil
}

// OldestPendingBySession reads the oldest checkpoint in each session's scope,
// including its workers, in one pass over pending checkpoints.
func (s *SQLStore) OldestPendingBySession(ctx context.Context) (map[string]time.Time, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	rows, err := s.queries.ListPendingCheckpointSessions(ctx)
	if err != nil {
		return nil, fmt.Errorf("list pending checkpoint sessions: %w", err)
	}
	out := make(map[string]time.Time, len(rows))
	for _, row := range rows {
		issued, err := db.ParseTime(row.OldestCreatedAt)
		if err != nil {
			return nil, fmt.Errorf("pending checkpoint %s created_at: %w", row.SessionID, err)
		}
		out[row.SessionID] = issued
	}
	return out, nil
}

// ListRejectedToolApprovals returns the denials resolved after their chat's
// latest user intent boundary; earlier denials no longer coalesce anything.
func (s *SQLStore) ListRejectedToolApprovals(ctx context.Context) ([]StoredCheckpoint, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	rows, err := s.queries.ListRejectedToolApprovalCheckpoints(ctx)
	if err != nil {
		return nil, fmt.Errorf("list rejected tool approvals: %w", err)
	}
	out := make([]StoredCheckpoint, 0, len(rows))
	for _, stored := range rows {
		row, err := checkpointFromRow(stored)
		if err != nil {
			return nil, err
		}
		out = append(out, *row)
	}
	return out, nil
}

// SetPendingAIRationale writes Payload["ai_rationale"] only while the row is pending.
func (s *SQLStore) SetPendingAIRationale(ctx context.Context, checkpointID, text string) (bool, error) {
	return s.mutatePendingPayload(ctx, checkpointID, "ai_rationale patch", func(payload map[string]any) bool {
		payload["ai_rationale"] = text
		delete(payload, "ai_rationale_pending")
		return true
	})
}

// ClearPendingAIRationaleFlag removes Payload["ai_rationale_pending"] only while the
// row is pending. A no-op (applied=false) when the row is missing, resolved, or the
// flag was already absent.
func (s *SQLStore) ClearPendingAIRationaleFlag(ctx context.Context, checkpointID string) (bool, error) {
	return s.mutatePendingPayload(ctx, checkpointID, "ai_rationale clear", func(payload map[string]any) bool {
		if _, pending := payload["ai_rationale_pending"]; !pending {
			return false
		}
		delete(payload, "ai_rationale_pending")
		return true
	})
}

// SetPendingJoined writes joined_count / joined_tool_call_ids only while pending.
func (s *SQLStore) SetPendingJoined(ctx context.Context, checkpointID string, joinedCount int, joinedToolCallIDs []string, joinerBand string, joinerCode string) (bool, error) {
	if joinedCount < 1 {
		joinedCount = 1
	}
	ids := append([]string(nil), joinedToolCallIDs...)
	return s.mutatePendingPayload(ctx, checkpointID, "joined patch", func(payload map[string]any) bool {
		payload["joined_count"] = joinedCount
		if len(ids) > 0 {
			payload["joined_tool_call_ids"] = ids
		}
		currentBand := stringField(payload, "consequence_band")
		currentCode := stringField(payload, "consequence_code")
		mergedBand, mergedCode := MaxConsequence(
			api.ConsequenceBand(currentBand), api.ConsequenceCode(currentCode),
			api.ConsequenceBand(joinerBand), api.ConsequenceCode(joinerCode),
		)
		if mergedBand == api.ConsequenceBandHighRisk {
			if string(mergedBand) != currentBand {
				payload["consequence_band"] = string(mergedBand)
			}
			if mergedCode != "" && string(mergedCode) != currentCode {
				payload["consequence_code"] = string(mergedCode)
			}
		}
		return true
	})
}

// mutatePendingPayload applies mutate to a still-pending checkpoint's payload map.
// mutate returns false to leave the row untouched (applied=false).
func (s *SQLStore) mutatePendingPayload(ctx context.Context, checkpointID, op string, mutate func(map[string]any) bool) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin %s: %w", op, err)
	}
	defer func() { _ = tx.Rollback() }()
	qtx := s.queries.WithTx(tx)

	current, err := qtx.GetCheckpoint(ctx, checkpointID)
	if db.IsNoRows(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("load checkpoint for %s: %w", op, err)
	}
	if DecisionStatus(current.Status) != DecisionStatusPending || current.Kind != string(api.CheckpointKindToolApproval) {
		return false, nil
	}
	payload := map[string]any{}
	if err := db.UnmarshalJSON(current.PayloadJson, &payload); err != nil {
		return false, err
	}
	if payload == nil {
		payload = map[string]any{}
	}
	if !mutate(payload) {
		return false, nil
	}
	outJSON, err := db.MarshalJSON(payload)
	if err != nil {
		return false, fmt.Errorf("marshal payload: %w", err)
	}
	n, err := qtx.UpdatePendingCheckpointPayload(ctx, db.UpdatePendingCheckpointPayloadParams{
		PayloadJson: outJSON,
		ID:          checkpointID,
		Status:      string(DecisionStatusPending),
	})
	if err != nil {
		return false, fmt.Errorf("update for %s: %w", op, err)
	}
	if n == 0 {
		return false, nil
	}
	current.PayloadJson = outJSON
	row, err := checkpointFromRow(current)
	if err != nil {
		return false, err
	}
	if err := s.enqueueCheckpointTx(ctx, tx, *row); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit %s: %w", op, err)
	}
	s.outbox.Notify()
	return true, nil
}

// LatestResolvedToolApprovalStatus returns the most recent resolved tool_approval status.
func (s *SQLStore) LatestResolvedToolApprovalStatus(ctx context.Context, sessionID string) (DecisionStatus, bool, error) {
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	status, err := s.queries.LatestResolvedCheckpointStatus(ctx, db.LatestResolvedCheckpointStatusParams{
		SessionID: sessionID,
		Kind:      string(api.CheckpointKindToolApproval),
		Status:    string(DecisionStatusPending),
	})
	if db.IsNoRows(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("latest resolved tool approval: %w", err)
	}
	return DecisionStatus(status), true, nil
}

// checkpointFromRow maps a generated checkpoints row onto the store's domain type.
func checkpointFromRow(r db.Checkpoints) (*StoredCheckpoint, error) {
	row := StoredCheckpoint{
		ID:          r.ID,
		SessionID:   r.SessionID,
		ProjectID:   r.ProjectID,
		ProjectDir:  r.ProjectDir,
		Kind:        api.CheckpointKind(r.Kind),
		Status:      DecisionStatus(r.Status),
		Type:        DecisionType(r.Type),
		Title:       r.Title,
		Description: r.Description,
		ToolName:    r.ToolName,
		Path:        r.Path,
	}
	if err := db.UnmarshalJSON(r.ArgsJson, &row.Args); err != nil {
		return nil, err
	}
	if err := db.UnmarshalJSON(r.FilesJson, &row.Files); err != nil {
		return nil, err
	}
	if err := db.UnmarshalJSON(r.PayloadJson, &row.Payload); err != nil {
		return nil, err
	}
	if r.ResultJson.Valid && r.ResultJson.String != "" {
		var wrapper map[string]any
		if err := db.UnmarshalJSON(r.ResultJson, &wrapper); err != nil {
			return nil, err
		}
		if raw, ok := wrapper["tool"]; ok {
			if b, err := json.Marshal(raw); err == nil {
				var dr DecisionResult
				if json.Unmarshal(b, &dr) == nil {
					row.Result = &dr
				}
			}
		}
		if raw, ok := wrapper["content_apply"]; ok {
			if b, err := json.Marshal(raw); err == nil {
				var cr ContentApplyResolve
				if json.Unmarshal(b, &cr) == nil {
					row.ContentResult = &cr
				}
			}
		}
		if row.Result == nil && row.ContentResult == nil {
			var dr DecisionResult
			if err := db.UnmarshalJSON(r.ResultJson, &dr); err == nil && (dr.Approved || dr.Comments != "") {
				row.Result = &dr
			}
		}
	}
	t, err := db.ParseTime(r.CreatedAt)
	if err != nil {
		return nil, err
	}
	row.CreatedAt = t
	row.ResolvedAt, err = db.TimePtrFromNull(r.ResolvedAt)
	if err != nil {
		return nil, err
	}
	if r.ResolvedBy.Valid && r.ResolvedBy.String != "" {
		row.Resolution = &Resolution{By: r.ResolvedBy.String, PersonID: db.StringFromNull(r.ResolvedByPersonID)}
	}
	return &row, nil
}

func resolutionBy(r *Resolution) string {
	if r == nil {
		return ""
	}
	return r.By
}

func resolutionPersonID(r *Resolution) string {
	if r == nil {
		return ""
	}
	return r.PersonID
}

func resolutionPolicy(r *Resolution) *authzledger.PolicyIdentity {
	if r == nil {
		return nil
	}
	return r.Policy
}

// MaxConsequence merges two band/code pairs for coalesced cards. Band never lowers;
// when both sides are high_risk the code follows fixed priority secret → detection → write root.
func MaxConsequence(aBand api.ConsequenceBand, aCode api.ConsequenceCode, bBand api.ConsequenceBand, bCode api.ConsequenceCode) (api.ConsequenceBand, api.ConsequenceCode) {
	band := aBand
	if bBand == api.ConsequenceBandHighRisk {
		band = api.ConsequenceBandHighRisk
	}
	if band != api.ConsequenceBandHighRisk {
		return api.ConsequenceBandStandard, ""
	}
	return api.ConsequenceBandHighRisk, maxConsequenceCode(aCode, bCode)
}

func maxConsequenceCode(a, b api.ConsequenceCode) api.ConsequenceCode {
	if consequenceCodeRank(b) > consequenceCodeRank(a) {
		return b
	}
	return a
}

func consequenceCodeRank(code api.ConsequenceCode) int {
	switch api.ConsequenceCode(strings.TrimSpace(string(code))) {
	case api.ConsequenceCodeSecret:
		return 3
	case api.ConsequenceCodeDetection:
		return 2
	case api.ConsequenceCodeWriteRoot:
		return 1
	default:
		return 0
	}
}

// ListPendingForParent reads pending checkpoints for a coordinator and its children.
func (s *SQLStore) ListPendingForParent(ctx context.Context, sessionID string, kind *api.CheckpointKind) ([]StoredCheckpoint, error) {
	params := db.ListParentPendingCheckpointsParams{ParentSessionID: sessionID}
	if kind != nil {
		params.Kind = string(*kind)
	}
	rows, err := s.queries.ListParentPendingCheckpoints(ctx, params)
	if err != nil {
		return nil, err
	}
	out := make([]StoredCheckpoint, 0, len(rows))
	for _, stored := range rows {
		row, err := checkpointFromRow(stored)
		if err != nil {
			return nil, err
		}
		out = append(out, *row)
	}
	return out, nil
}
