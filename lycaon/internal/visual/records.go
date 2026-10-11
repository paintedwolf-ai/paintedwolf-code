package visual

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

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

// ArtifactRecord is one durable artifact row.
type ArtifactRecord struct {
	ID              string `json:"id"`
	ProjectID       string `json:"project_id"`
	RootSessionID   string `json:"root_session_id"`
	SessionID       string `json:"session_id"`
	WorkflowRunID   string `json:"workflow_run_id"`
	ToolCallID      string `json:"tool_call_id"`
	OriginMessageID string `json:"origin_message_id"`
	OperationID     string `json:"operation_id,omitempty"`
	NaturalKey      string `json:"natural_key,omitempty"`
	ContentHash     string `json:"content_hash"`
	RetentionClass  string `json:"retention_class"`
	ByteSize        int64  `json:"byte_size"`
	StoredSize      int64  `json:"stored_size"`
	Mime            string `json:"mime"`
	Source          string `json:"source"`
	Caption         string `json:"caption"`
	EvidenceHandle  string `json:"evidence_handle"`
	PageID          string `json:"page_id,omitempty"`
	Perceive        bool   `json:"perceive,omitempty"`
	DurationMS      int64  `json:"duration_ms,omitempty"`
	RecordedAt      string `json:"recorded_at,omitempty"`
	CreatedAt       string `json:"created_at"`
	UpdatedAt       string `json:"updated_at,omitempty"`
	DeletedAt       string `json:"deleted_at,omitempty"`
	DeletedReason   string `json:"deleted_reason,omitempty"`
	Width           int    `json:"width,omitempty"`
	Height          int    `json:"height,omitempty"`
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
}

// NewRecords binds the durable record store to the main database.
func NewRecords(sqlDB db.Handle, outbox *eventoutbox.Outbox, projection ArtifactProjection) *Records {
	if sqlDB == nil {
		return nil
	}
	return &Records{sqlDB: sqlDB, queries: db.New(sqlDB), outbox: outbox, projection: projection}
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
func (r *Records) BindEvidenceHandleTx(ctx context.Context, tx *sql.Tx, projectID, artifactID, handle string) error {
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

// SoftDelete tombstones an artifact and returns its references.
func (r *Records) SoftDelete(ctx context.Context, projectID, artifactID, reason string) (ArtifactRecord, []ArtifactReference, error) {
	if err := r.writable(); err != nil {
		return ArtifactRecord{}, nil, err
	}
	projectID = strings.TrimSpace(projectID)
	artifactID = strings.TrimSpace(artifactID)
	rec, found, err := r.GetInProject(ctx, projectID, artifactID)
	if err != nil || !found {
		return ArtifactRecord{}, nil, err
	}
	refs, err := r.References(ctx, artifactID)
	if err != nil {
		return ArtifactRecord{}, nil, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	tx, err := r.sqlDB.BeginTx(ctx, nil)
	if err != nil {
		return ArtifactRecord{}, nil, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := guardDeletion(ctx, tx); err != nil {
		return ArtifactRecord{}, nil, err
	}
	qtx := r.queries.WithTx(tx)
	affected, err := qtx.SoftDeleteArtifact(ctx, db.SoftDeleteArtifactParams{
		DeletedAt:     nullString(now),
		DeletedReason: nullString(reason),
		UpdatedAt:     now,
		ProjectID:     projectID,
		ID:            artifactID,
	})
	if err != nil {
		return ArtifactRecord{}, nil, err
	}
	if affected == 0 {
		// Preserve the first deletion time.
		return rec, refs, nil
	}
	if drop := r.projection.Delete; drop != nil {
		if err := drop(ctx, tx, artifactID); err != nil {
			return ArtifactRecord{}, nil, err
		}
	}
	rec.DeletedAt = now
	rec.DeletedReason = strings.TrimSpace(reason)
	if err := r.enqueueTx(ctx, tx, rec, api.ArtifactChangeOpDeleted); err != nil {
		return ArtifactRecord{}, nil, err
	}
	if err := tx.Commit(); err != nil {
		return ArtifactRecord{}, nil, err
	}
	r.outbox.Notify()
	return rec, refs, nil
}

// Discard removes a record that has no durable references.
func (r *Records) Discard(ctx context.Context, projectID, artifactID string) (ArtifactRecord, bool, error) {
	if !r.ready() {
		return ArtifactRecord{}, false, fmt.Errorf("artifact records not configured")
	}
	projectID = strings.TrimSpace(projectID)
	artifactID = strings.TrimSpace(artifactID)
	tx, err := r.sqlDB.BeginTx(ctx, nil)
	if err != nil {
		return ArtifactRecord{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	queries := r.queries.WithTx(tx)
	row, err := queries.GetArtifactInProject(ctx, db.GetArtifactInProjectParams{ProjectID: projectID, ID: artifactID})
	found, err := foundOrErr(err)
	if err != nil || !found {
		return ArtifactRecord{}, false, err
	}
	rec := recordFrom(row)
	if drop := r.projection.Delete; drop != nil {
		if err := drop(ctx, tx, artifactID); err != nil {
			return ArtifactRecord{}, false, err
		}
	}
	deleted, err := queries.DeleteUnreferencedArtifact(ctx, db.DeleteUnreferencedArtifactParams{
		ProjectID: projectID,
		ID:        artifactID,
	})
	if err != nil {
		return ArtifactRecord{}, false, err
	}
	if deleted == 0 {
		return ArtifactRecord{}, false, fmt.Errorf("artifact %s has durable references", artifactID)
	}
	if err := r.enqueueTx(ctx, tx, rec, api.ArtifactChangeOpDeleted); err != nil {
		return ArtifactRecord{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return ArtifactRecord{}, false, err
	}
	r.outbox.Notify()
	return rec, true, nil
}

func (r *Records) enqueueTx(ctx context.Context, tx *sql.Tx, rec ArtifactRecord, op api.ArtifactChangeOp) error {
	return r.outbox.EnqueueTx(ctx, tx, api.EventTopicArtifact,
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

// ListProject returns one bounded page of live project records, oldest first.
func (r *Records) ListProject(ctx context.Context, projectID string, query ArtifactPageQuery) ([]ArtifactRecord, bool, error) {
	if !r.ready() {
		return nil, false, nil
	}
	limit := query.effectiveLimit()
	rows, err := r.queries.PageProjectArtifacts(ctx, db.PageProjectArtifactsParams{
		ProjectID: strings.TrimSpace(projectID), AfterCreatedAt: query.AfterCreatedAt,
		AfterID: query.AfterID, PageLimit: int64(limit + 1),
	})
	if err != nil {
		return nil, false, err
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	out := make([]ArtifactRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, recordFrom(row))
	}
	return out, hasMore, nil
}

func artifactIDs(records []ArtifactRecord) []string {
	ids := make([]string, 0, len(records))
	for _, record := range records {
		ids = append(ids, record.ID)
	}
	return ids
}

// ReferenceCountsFor returns claim counts only for the requested page.
func (r *Records) ReferenceCountsFor(ctx context.Context, projectID string, records []ArtifactRecord) (map[string][]api.ArtifactReferenceCount, error) {
	out := make(map[string][]api.ArtifactReferenceCount, len(records))
	if len(records) == 0 {
		return out, nil
	}
	rows, err := r.queries.ListProjectArtifactRefCountsFor(ctx, db.ListProjectArtifactRefCountsForParams{
		ProjectID: strings.TrimSpace(projectID), ArtifactIds: artifactIDs(records),
	})
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		out[row.ArtifactID] = append(out[row.ArtifactID], api.ArtifactReferenceCount{
			Kind: api.ArtifactReferenceKind(row.Kind), Count: int(row.RefCount),
		})
	}
	return out, nil
}

// ProjectOriginsFor returns first transcript origins only for the requested page.
func (r *Records) ProjectOriginsFor(ctx context.Context, projectID string, records []ArtifactRecord) (map[string]ArtifactOrigin, error) {
	out := make(map[string]ArtifactOrigin, len(records))
	if len(records) == 0 {
		return out, nil
	}
	rows, err := r.queries.ListProjectArtifactOriginsFor(ctx, db.ListProjectArtifactOriginsForParams{
		ProjectID: strings.TrimSpace(projectID), ArtifactIds: artifactIDs(records),
	})
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		if _, exists := out[row.ArtifactID]; !exists {
			out[row.ArtifactID] = ArtifactOrigin{MessageID: row.MessageID.String, ToolCallID: row.ToolCallID}
		}
	}
	return out, nil
}

// ListTree returns live records produced under one session tree, oldest first.
func (r *Records) ListTree(ctx context.Context, projectID, rootSessionID string) ([]ArtifactRecord, error) {
	if !r.ready() {
		return nil, nil
	}
	rows, err := r.queries.ListTreeArtifacts(ctx, db.ListTreeArtifactsParams{
		ProjectID:     strings.TrimSpace(projectID),
		RootSessionID: nullString(rootSessionID),
	})
	if err != nil {
		return nil, err
	}
	out := make([]ArtifactRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, recordFrom(row))
	}
	return out, nil
}

// FindByEvidenceHandle resolves the newest live artifact carrying a handle in
// one session tree.
func (r *Records) FindByEvidenceHandle(ctx context.Context, rootSessionID, handle string) (ArtifactRecord, bool, error) {
	if !r.ready() {
		return ArtifactRecord{}, false, nil
	}
	row, err := r.queries.FindTreeArtifactByEvidenceHandle(ctx, db.FindTreeArtifactByEvidenceHandleParams{
		RootSessionID:  nullString(rootSessionID),
		EvidenceHandle: strings.TrimSpace(handle),
	})
	found, err := foundOrErr(err)
	return recordFrom(row), found, err
}

// References lists every durable claim on an artifact.
func (r *Records) References(ctx context.Context, artifactID string) ([]ArtifactReference, error) {
	if !r.ready() {
		return nil, nil
	}
	rows, err := r.queries.ListArtifactRefs(ctx, strings.TrimSpace(artifactID))
	if err != nil {
		return nil, err
	}
	out := make([]ArtifactReference, 0, len(rows))
	for _, row := range rows {
		ref := ArtifactReference{
			Kind:       api.ArtifactReferenceKind(row.Kind),
			MessageID:  row.MessageID.String,
			SessionID:  row.SessionID.String,
			ToolCallID: row.ToolCallID,
		}
		if ts, perr := time.Parse(time.RFC3339Nano, row.CreatedAt); perr == nil {
			ref.CreatedAt = ts
		}
		out = append(out, ref)
	}
	return out, nil
}

// ArtifactOrigin is the transcript row that first claimed an artifact. Both
// halves travel together: a tool call names a tool row, a message id names
// every other row.
type ArtifactOrigin struct {
	MessageID  string
	ToolCallID string
}

// TreeOrigins maps artifact id to first referring row within one tree.
func (r *Records) TreeOrigins(ctx context.Context, rootSessionID string) (map[string]ArtifactOrigin, error) {
	if !r.ready() {
		return nil, nil
	}
	rows, err := r.queries.ListTreeArtifactOrigins(ctx, nullString(rootSessionID))
	if err != nil {
		return nil, err
	}
	out := make(map[string]ArtifactOrigin, len(rows))
	for _, row := range rows {
		if _, seen := out[row.ArtifactID]; !seen {
			out[row.ArtifactID] = ArtifactOrigin{
				MessageID:  row.MessageID.String,
				ToolCallID: row.ToolCallID,
			}
		}
	}
	return out, nil
}

// HashIsOrphan reports whether a content blob can be removed.
func (r *Records) HashIsOrphan(ctx context.Context, projectID, hash, excludeID string) (bool, error) {
	if !r.ready() {
		return false, nil
	}
	count, err := r.queries.CountArtifactsWithContentHash(ctx, db.CountArtifactsWithContentHashParams{
		ProjectID:   strings.TrimSpace(projectID),
		ContentHash: strings.TrimSpace(hash),
		ID:          strings.TrimSpace(excludeID),
	})
	if err != nil {
		return false, err
	}
	return count == 0, nil
}

// ProjectBytes is the durable byte total for one project's live artifacts.
func (r *Records) ProjectBytes(ctx context.Context, projectID string) (int64, error) {
	if !r.ready() {
		return 0, nil
	}
	return r.queries.SumProjectArtifactBytes(ctx, strings.TrimSpace(projectID))
}

// RefRowID is the deterministic key for one claim.
func RefRowID(kind api.ArtifactReferenceKind, artifactID, messageID, sessionID, toolCallID string) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{
		string(kind),
		strings.TrimSpace(artifactID),
		strings.TrimSpace(messageID),
		strings.TrimSpace(sessionID),
		strings.TrimSpace(toolCallID),
	}, "\x00")))
	return hex.EncodeToString(sum[:16])
}

// RefWrite is one claim to record inside a referrer's transaction.
type RefWrite struct {
	Kind       api.ArtifactReferenceKind
	ArtifactID string
	ProjectID  string
	MessageID  string
	SessionID  string
	ToolCallID string
}

// WriteRefsTx records valid project claims in the caller's transaction.
func WriteRefsTx(ctx context.Context, qtx *db.Queries, refs []RefWrite) error {
	if qtx == nil || len(refs) == 0 {
		return nil
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, ref := range refs {
		artifactID := strings.TrimSpace(ref.ArtifactID)
		projectID := strings.TrimSpace(ref.ProjectID)
		if artifactID == "" || projectID == "" || ref.Kind == "" {
			continue
		}
		if err := qtx.UpsertArtifactRef(ctx, db.UpsertArtifactRefParams{
			ID:         RefRowID(ref.Kind, artifactID, ref.MessageID, ref.SessionID, ref.ToolCallID),
			Kind:       string(ref.Kind),
			MessageID:  nullString(ref.MessageID),
			SessionID:  nullString(ref.SessionID),
			ToolCallID: strings.TrimSpace(ref.ToolCallID),
			CreatedAt:  now,
			ID_2:       artifactID,
			ProjectID:  projectID,
		}); err != nil {
			return err
		}
	}
	return nil
}

// ClearMessageRefsTx resets a message's claims.
func ClearMessageRefsTx(ctx context.Context, qtx *db.Queries, messageID string) error {
	if qtx == nil || strings.TrimSpace(messageID) == "" {
		return nil
	}
	return qtx.DeleteMessageArtifactRefs(ctx, nullString(messageID))
}

// ClearProjectRefsOfKindTx releases one project claim kind.
func ClearProjectRefsOfKindTx(ctx context.Context, qtx *db.Queries, projectID string, kind api.ArtifactReferenceKind) error {
	if qtx == nil || strings.TrimSpace(projectID) == "" {
		return nil
	}
	return qtx.DeleteProjectArtifactRefsOfKind(ctx, db.DeleteProjectArtifactRefsOfKindParams{
		ProjectID: strings.TrimSpace(projectID),
		Kind:      string(kind),
	})
}

func normalizeRecord(rec ArtifactRecord) ArtifactRecord {
	rec.ID = strings.TrimSpace(rec.ID)
	rec.ProjectID = strings.TrimSpace(rec.ProjectID)
	rec.RootSessionID = strings.TrimSpace(rec.RootSessionID)
	rec.SessionID = strings.TrimSpace(rec.SessionID)
	rec.WorkflowRunID = strings.TrimSpace(rec.WorkflowRunID)
	rec.OriginMessageID = strings.TrimSpace(rec.OriginMessageID)
	rec.ContentHash = strings.ToLower(strings.TrimSpace(rec.ContentHash))
	rec.Mime = strings.TrimSpace(rec.Mime)
	rec.Source = strings.TrimSpace(rec.Source)
	if rec.CreatedAt == "" {
		rec.CreatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	rec.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	return rec
}

func upsertParams(rec ArtifactRecord) db.UpsertArtifactParams {
	return db.UpsertArtifactParams{
		ID:              rec.ID,
		ProjectID:       rec.ProjectID,
		RootSessionID:   nullString(rec.RootSessionID),
		SessionID:       nullString(rec.SessionID),
		WorkflowRunID:   nullString(rec.WorkflowRunID),
		ToolCallID:      strings.TrimSpace(rec.ToolCallID),
		OriginMessageID: strings.TrimSpace(rec.OriginMessageID),
		OperationID:     nullString(rec.OperationID),
		NaturalKey:      nullString(rec.NaturalKey),
		ContentHash:     rec.ContentHash,
		RetentionClass:  artifactRetentionClass(rec.RetentionClass),
		ByteSize:        rec.ByteSize,
		StoredSize:      rec.StoredSize,
		Mime:            rec.Mime,
		Source:          rec.Source,
		Caption:         rec.Caption,
		EvidenceHandle:  strings.TrimSpace(rec.EvidenceHandle),
		PageID:          strings.TrimSpace(rec.PageID),
		Perceive:        boolToInt64(rec.Perceive),
		DurationMs:      rec.DurationMS,
		RecordedAt:      nullString(rec.RecordedAt),
		CreatedAt:       rec.CreatedAt,
		UpdatedAt:       rec.UpdatedAt,
		Width:           int64(rec.Width),
		Height:          int64(rec.Height),
	}
}

func recordFrom(row db.Artifacts) ArtifactRecord {
	return ArtifactRecord{
		ID:              row.ID,
		ProjectID:       row.ProjectID,
		RootSessionID:   row.RootSessionID.String,
		SessionID:       row.SessionID.String,
		WorkflowRunID:   row.WorkflowRunID.String,
		ToolCallID:      row.ToolCallID,
		OriginMessageID: row.OriginMessageID,
		OperationID:     row.OperationID.String,
		NaturalKey:      row.NaturalKey.String,
		ContentHash:     row.ContentHash,
		RetentionClass:  row.RetentionClass,
		ByteSize:        row.ByteSize,
		StoredSize:      row.StoredSize,
		Mime:            row.Mime,
		Source:          row.Source,
		Caption:         row.Caption,
		EvidenceHandle:  row.EvidenceHandle,
		PageID:          row.PageID,
		Perceive:        row.Perceive != 0,
		DurationMS:      row.DurationMs,
		RecordedAt:      row.RecordedAt.String,
		CreatedAt:       row.CreatedAt,
		UpdatedAt:       row.UpdatedAt,
		DeletedAt:       row.DeletedAt.String,
		DeletedReason:   row.DeletedReason.String,
		Width:           int(row.Width),
		Height:          int(row.Height),
	}
}

func foundOrErr(err error) (bool, error) {
	if err == nil {
		return true, nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return false, err
}

func nullString(s string) sql.NullString {
	s = strings.TrimSpace(s)
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}

func boolToInt64(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

func artifactRetentionClass(class string) string {
	if class == "recording" {
		return class
	}
	return "artifact"
}
