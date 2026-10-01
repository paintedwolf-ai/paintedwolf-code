package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/pkg/api"
)

type RewindOperation struct {
	ID              string
	SessionID       string
	AnchorMessageID string
	InputDigest     string
	ProjectDir      string
	JournalPath     string
	Status          string
	Error           string
	ResponseJSON    string
	// CheckpointAnchorIDs retains the committed cleanup worklist across interruptions.
	CheckpointAnchorIDs []string
}

// RewindOperationSweepItem identifies committed journal and anchor cleanup.
type RewindOperationSweepItem struct {
	ID                  string
	SessionID           string
	AnchorMessageID     string
	ProjectDir          string
	JournalPath         string
	CheckpointAnchorIDs []string
	CreatedAt           time.Time
}

func encodeCheckpointAnchorIDs(ids []string) (string, error) {
	if len(ids) == 0 {
		return "[]", nil
	}
	raw, err := json.Marshal(ids)
	if err != nil {
		return "", fmt.Errorf("encode checkpoint anchor ids: %w", err)
	}
	return string(raw), nil
}

func decodeCheckpointAnchorIDs(raw string) ([]string, error) {
	var ids []string
	if err := json.Unmarshal([]byte(raw), &ids); err != nil {
		return nil, fmt.Errorf("decode checkpoint anchor ids: %w", err)
	}
	if ids == nil {
		return nil, fmt.Errorf("checkpoint anchor ids must be an array")
	}
	for _, id := range ids {
		if strings.TrimSpace(id) == "" {
			return nil, fmt.Errorf("checkpoint anchor id is empty")
		}
	}
	return ids, nil
}

func (s *SQL) PrepareRewind(ctx context.Context, op RewindOperation) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("rewind store not configured")
	}
	anchorIDsJSON, err := encodeCheckpointAnchorIDs(op.CheckpointAnchorIDs)
	if err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	rows, err := s.queries.PrepareRewindOperation(ctx, db.PrepareRewindOperationParams{
		ID: op.ID, SessionID: op.SessionID, AnchorMessageID: op.AnchorMessageID,
		InputDigest: op.InputDigest, ProjectDir: op.ProjectDir, JournalPath: op.JournalPath,
		CheckpointAnchorIdsJson: anchorIDsJSON,
		CreatedAt:               now, UpdatedAt: now,
	})
	if err != nil {
		return err
	}
	if rows != 1 {
		return fmt.Errorf("rewind operation already exists")
	}
	return nil
}

func (s *SQL) GetRewindOperation(ctx context.Context, id string) (*RewindOperation, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("rewind store not configured")
	}
	row, err := s.queries.GetRewindOperation(ctx, id)
	if db.IsNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	anchorIDs, err := decodeCheckpointAnchorIDs(row.CheckpointAnchorIdsJson)
	if err != nil {
		return nil, err
	}
	return &RewindOperation{
		ID: row.ID, SessionID: row.SessionID, AnchorMessageID: row.AnchorMessageID,
		InputDigest: row.InputDigest, ProjectDir: row.ProjectDir, JournalPath: row.JournalPath,
		Status: row.Status, Error: row.Error, ResponseJSON: row.ResponseJson,
		CheckpointAnchorIDs: anchorIDs,
	}, nil
}

func (s *SQL) SetRewindPhase(ctx context.Context, id, status, detail string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("rewind store not configured")
	}
	switch status {
	case "applying", "files_applied", "rolled_back", "diverged":
	default:
		return fmt.Errorf("invalid rewind phase %q", status)
	}
	rows, err := s.queries.SetRewindOperationPhase(ctx, db.SetRewindOperationPhaseParams{
		Status: status, Error: strings.TrimSpace(detail),
		UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano), ID: id,
	})
	if err != nil {
		return err
	}
	if rows != 1 {
		return fmt.Errorf("rewind operation %s not mutable", id)
	}
	return nil
}

func (s *SQL) CommitRewind(ctx context.Context, id, sessionID, anchorMessageID string, response api.RewindSessionResponse) (int, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("rewind store not configured")
	}
	unlock := s.lockMutation(sessionID)
	defer unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	queries := s.queries.WithTx(tx)
	status, err := queries.GetRewindOperationStatus(ctx, db.GetRewindOperationStatusParams{
		ID: id, SessionID: sessionID, AnchorMessageID: anchorMessageID,
	})
	if err != nil {
		return 0, err
	}
	if status == "committed" {
		return 0, nil
	}
	if status != "files_applied" {
		return 0, fmt.Errorf("rewind operation %s is %s", id, status)
	}
	removed, sess, spillRefs, err := s.truncateMessagesTx(ctx, tx, sessionID, anchorMessageID)
	if err != nil {
		return 0, err
	}
	rootID, err := queries.GetRootSessionID(ctx, sessionID)
	if err != nil {
		return 0, err
	}
	if err := queries.DeleteSessionProgress(ctx, rootID); err != nil {
		return 0, err
	}
	if _, err := queries.CancelQueuedPromptSubmissionsBySession(ctx, db.CancelQueuedPromptSubmissionsBySessionParams{
		CompletedAt: db.NullString(time.Now().UTC().Format(time.RFC3339Nano)),
		SessionID:   sessionID,
	}); err != nil {
		return 0, err
	}
	if err := s.enqueueSessionEvent(ctx, tx, sess, api.SessionEventActionUpdated); err != nil {
		return 0, err
	}
	response.TruncatedMessageCount = int(removed)
	raw, err := json.Marshal(response)
	if err != nil {
		return 0, err
	}
	rows, err := queries.CommitRewindOperation(ctx, db.CommitRewindOperationParams{
		ResponseJson: sql.NullString{String: string(raw), Valid: true},
		UpdatedAt:    time.Now().UTC().Format(time.RFC3339Nano), ID: id,
	})
	if err != nil {
		return 0, err
	}
	if rows != 1 {
		return 0, fmt.Errorf("rewind operation %s lost its commit claim", id)
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	s.reclaimUnreferencedSpills(ctx, spillRefs)
	s.outbox.Notify()
	return int(removed), nil
}

func (s *SQL) RewindOperationsForRecovery(ctx context.Context) ([]RewindOperation, error) {
	rows, err := s.queries.ListRewindOperationsForRecovery(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]RewindOperation, 0, len(rows))
	for _, row := range rows {
		anchorIDs, err := decodeCheckpointAnchorIDs(row.CheckpointAnchorIdsJson)
		if err != nil {
			return nil, err
		}
		out = append(out, RewindOperation{
			ID: row.ID, SessionID: row.SessionID, AnchorMessageID: row.AnchorMessageID,
			InputDigest: row.InputDigest, ProjectDir: row.ProjectDir, JournalPath: row.JournalPath,
			Status: row.Status, Error: row.Error, ResponseJSON: row.ResponseJson,
			CheckpointAnchorIDs: anchorIDs,
		})
	}
	return out, nil
}

// RewindOperationsForRecoverySession scopes RewindOperationsForRecovery to
// one session, for on-demand repair after a panic caught mid-rewind.
func (s *SQL) RewindOperationsForRecoverySession(ctx context.Context, sessionID string) ([]RewindOperation, error) {
	rows, err := s.queries.ListRewindOperationsForRecoverySession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	out := make([]RewindOperation, 0, len(rows))
	for _, row := range rows {
		anchorIDs, err := decodeCheckpointAnchorIDs(row.CheckpointAnchorIdsJson)
		if err != nil {
			return nil, err
		}
		out = append(out, RewindOperation{
			ID: row.ID, SessionID: row.SessionID, AnchorMessageID: row.AnchorMessageID,
			InputDigest: row.InputDigest, ProjectDir: row.ProjectDir, JournalPath: row.JournalPath,
			Status: row.Status, Error: row.Error, ResponseJSON: row.ResponseJson,
			CheckpointAnchorIDs: anchorIDs,
		})
	}
	return out, nil
}

// CommittedRewindOperationsForSweep lists committed rewinds awaiting the
// boot-only disk GC pass (see Manager.sweepCommittedRewinds).
func (s *SQL) CommittedRewindOperationsForSweep(ctx context.Context) ([]RewindOperationSweepItem, error) {
	rows, err := s.queries.ListCommittedRewindOperationsForSweep(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]RewindOperationSweepItem, 0, len(rows))
	for _, row := range rows {
		anchorIDs, err := decodeCheckpointAnchorIDs(row.CheckpointAnchorIdsJson)
		if err != nil {
			return nil, err
		}
		createdAt, err := db.ParseTime(row.CreatedAt)
		if err != nil {
			return nil, err
		}
		out = append(out, RewindOperationSweepItem{
			ID: row.ID, SessionID: row.SessionID, AnchorMessageID: row.AnchorMessageID,
			ProjectDir: row.ProjectDir, JournalPath: row.JournalPath,
			CheckpointAnchorIDs: anchorIDs, CreatedAt: createdAt,
		})
	}
	return out, nil
}

// DeleteRewindOperation removes one committed rewind row once its retention
// window (olderThan) has passed. A no-op (not an error) if the row already
// moved past 'committed', or hasn't aged out yet.
func (s *SQL) DeleteRewindOperation(ctx context.Context, id string, olderThan time.Time) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("rewind store not configured")
	}
	_, err := s.queries.DeleteRewindOperation(ctx, db.DeleteRewindOperationParams{
		ID: id, CreatedAt: olderThan.UTC().Format(time.RFC3339Nano),
	})
	return err
}
