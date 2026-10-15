package visual

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/eventoutbox"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/pkg/api"
)

// ArtifactProjection updates search inside record transactions.
type ArtifactProjection struct {
	Write  func(ctx context.Context, tx *sql.Tx, projectID string, rec ArtifactRecord) error
	Delete func(ctx context.Context, tx *sql.Tx, artifactID string) error
}

// ArtifactScope identifies the project, session tree, and operation that own an artifact.
type ArtifactScope struct {
	ID              string `json:"id"`
	ProjectID       string `json:"project_id"`
	RootSessionID   string `json:"root_session_id"`
	SessionID       string `json:"session_id"`
	WorkflowRunID   string `json:"workflow_run_id"`
	ToolCallID      string `json:"tool_call_id"`
	OriginMessageID string `json:"origin_message_id"`
	OperationID     string `json:"operation_id,omitempty"`
	NaturalKey      string `json:"natural_key,omitempty"`
}

// ArtifactContent describes the stored payload and media presentation of an artifact.
type ArtifactContent struct {
	ContentHash    string `json:"content_hash"`
	RetentionClass string `json:"retention_class"`
	ByteSize       int64  `json:"byte_size"`
	StoredSize     int64  `json:"stored_size"`
	Mime           string `json:"mime"`
	Source         string `json:"source"`
	Caption        string `json:"caption"`
	Width          int    `json:"width,omitempty"`
	Height         int    `json:"height,omitempty"`
}

// ArtifactLifecycle tracks evidence binding, perception, and deletion state of an artifact.
type ArtifactLifecycle struct {
	EvidenceHandle string `json:"evidence_handle"`
	PageID         string `json:"page_id,omitempty"`
	Perceive       bool   `json:"perceive,omitempty"`
	DurationMS     int64  `json:"duration_ms,omitempty"`
	RecordedAt     string `json:"recorded_at,omitempty"`
	CreatedAt      string `json:"created_at"`
	UpdatedAt      string `json:"updated_at,omitempty"`
	DeletedAt      string `json:"deleted_at,omitempty"`
	DeletedReason  string `json:"deleted_reason,omitempty"`
}

// ArtifactRecord is one durable artifact row.
type ArtifactRecord struct {
	ArtifactScope
	ArtifactContent
	ArtifactLifecycle
}

// Deleted reports whether the artifact has a durable tombstone.
func (r ArtifactRecord) Deleted() bool { return strings.TrimSpace(r.DeletedAt) != "" }

// Records stores artifact rows, references, projections, and events.
type Records struct {
	sqlDB      db.Handle
	queries    *db.Queries
	outbox     *eventoutbox.Outbox
	projection ArtifactProjection
	// onHandleBound keeps the hot tier coherent after a binding commits.
	onHandleBound func(context.Context, string, string)
	recordsCatalog
}

type recordsCatalog struct {
	r *Records
}

// NewRecords binds the durable record store to the main database.
func NewRecords(sqlDB db.Handle, outbox *eventoutbox.Outbox, projection ArtifactProjection) *Records {
	if sqlDB == nil {
		return nil
	}
	r := &Records{sqlDB: sqlDB, queries: db.New(sqlDB), outbox: outbox, projection: projection}
	r.r = r
	return r
}

func (r *Records) ready() bool { return r != nil && r.sqlDB != nil }

// writable requires durable records and events.
func (r *Records) writable() error {
	if !r.ready() {
		return fmt.Errorf("artifact records not configured")
	}
	if r.outbox == nil {
		return fmt.Errorf("artifact records: %w", eventoutbox.ErrNotWired)
	}
	return nil
}

// ErrArtifactDeleted protects tombstoned IDs.
var ErrArtifactDeleted = errors.New("artifact is deleted")

// Commit writes a record, projection, and event atomically.
func (r *Records) Commit(ctx context.Context, rec ArtifactRecord) error {
	if err := r.writable(); err != nil {
		return err
	}
	rec = normalizeRecord(rec)
	if rec.ID == "" || rec.ProjectID == "" || rec.ContentHash == "" {
		return fmt.Errorf("artifact id, project id, and content hash required")
	}
	tx, err := r.sqlDB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	qtx := r.queries.WithTx(tx)
	written, err := qtx.UpsertArtifact(ctx, upsertParams(rec))
	if err != nil {
		return err
	}
	if written == 0 {
		return fmt.Errorf("%w: %s", ErrArtifactDeleted, rec.ID)
	}
	if write := r.projection.Write; write != nil {
		if err := write(ctx, tx, rec.ProjectID, rec); err != nil {
			return err
		}
	}
	if err := r.enqueueTx(ctx, tx, rec, api.ArtifactChangeOpWritten); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	r.outbox.Notify()
	return nil
}

// BindEvidenceHandleTx stamps a host-minted evidence handle onto a live
// artifact inside the caller's transaction, so the handle and the ledger row
// commit together. A tombstoned artifact keeps no handle. Call
// EvidenceHandleBound after the transaction commits.
func (c recordsCatalog) BindEvidenceHandleTx(ctx context.Context, tx *sql.Tx, projectID, artifactID, handle string) error {
	r := c.r
	if err := r.writable(); err != nil {
		return err
	}
	projectID, artifactID, handle = strings.TrimSpace(projectID), strings.TrimSpace(artifactID), strings.TrimSpace(handle)
	if projectID == "" || artifactID == "" || handle == "" {
		return fmt.Errorf("artifact project, id, and evidence handle required")
	}
	qtx := r.queries.WithTx(tx)
	row, err := qtx.GetArtifactInProject(ctx, db.GetArtifactInProjectParams{ProjectID: projectID, ID: artifactID})
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("bind evidence handle %s: artifact %s not found in project", handle, artifactID)
	}
	if err != nil {
		return err
	}
	rec := recordFrom(row)
	if rec.Deleted() {
		return nil
	}
	rec.EvidenceHandle = handle
	rec = normalizeRecord(rec)
	if _, err := qtx.UpsertArtifact(ctx, upsertParams(rec)); err != nil {
		return err
	}
	if write := r.projection.Write; write != nil {
		if err := write(ctx, tx, rec.ProjectID, rec); err != nil {
			return err
		}
	}
	return r.enqueueTx(ctx, tx, rec, api.ArtifactChangeOpWritten)
}

// EvidenceHandleBound publishes a committed binding: event delivery wakes and
// the hot tier takes the handle.
func (r *Records) EvidenceHandleBound(ctx context.Context, artifactID, handle string) {
	if r == nil {
		return
	}
	if r.outbox != nil {
		r.outbox.Notify()
	}
	if r.onHandleBound != nil {
		r.onHandleBound(ctx, normalizeID(artifactID), strings.TrimSpace(handle))
	}
}

func (c recordsCatalog) enqueueTx(ctx context.Context, tx *sql.Tx, rec ArtifactRecord, op api.ArtifactChangeOp) error {
	return c.r.outbox.EnqueueTx(ctx, tx, api.EventTopicArtifact,
		events.PublishKey{Project: rec.ProjectID, Session: rec.RootSessionID},
		api.ArtifactEvent{
			ArtifactID:    rec.ID,
			ProjectID:     rec.ProjectID,
			SessionID:     rec.SessionID,
			RootSessionID: rec.RootSessionID,
			Op:            op,
		})
}

// Get returns one record regardless of project or tombstone state.
func (r *Records) Get(ctx context.Context, artifactID string) (ArtifactRecord, bool, error) {
	if !r.ready() {
		return ArtifactRecord{}, false, nil
	}
	row, err := r.queries.GetArtifact(ctx, strings.TrimSpace(artifactID))
	found, err := foundOrErr(err)
	return recordFrom(row), found, err
}

// GetInProject returns one record scoped to a project.
func (r *Records) GetInProject(ctx context.Context, projectID, artifactID string) (ArtifactRecord, bool, error) {
	if !r.ready() {
		return ArtifactRecord{}, false, nil
	}
	row, err := r.queries.GetArtifactInProject(ctx, db.GetArtifactInProjectParams{
		ProjectID: strings.TrimSpace(projectID),
		ID:        strings.TrimSpace(artifactID),
	})
	found, err := foundOrErr(err)
	return recordFrom(row), found, err
}

// ClaimedID resolves an operation or natural-key binding.
func (r *Records) ClaimedID(ctx context.Context, projectID, operationID, naturalKey string) (string, error) {
	if !r.ready() {
		return "", nil
	}
	projectID = strings.TrimSpace(projectID)
	if op := strings.TrimSpace(operationID); op != "" {
		row, err := r.queries.GetArtifactByOperation(ctx, db.GetArtifactByOperationParams{
			ProjectID: projectID, OperationID: nullString(op),
		})
		if found, err := foundOrErr(err); err != nil {
			return "", err
		} else if found {
			return row.ID, nil
		}
	}
	if key := strings.TrimSpace(naturalKey); key != "" {
		row, err := r.queries.GetArtifactByNaturalKey(ctx, db.GetArtifactByNaturalKeyParams{
			ProjectID: projectID, NaturalKey: nullString(key),
		})
		if found, err := foundOrErr(err); err != nil {
			return "", err
		} else if found {
			return row.ID, nil
		}
	}
	return "", nil
}

