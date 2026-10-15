package visual

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/db"
)

// ResolveInTree resolves one artifact inside a session tree.
func ResolveInTree(ctx context.Context, store Store, rootSessionID, artifactID string) Resolution {
	if store == nil {
		return Absent(AbsenceUnknown)
	}
	root := normalizeKey(rootSessionID)
	id := normalizeID(artifactID)
	if root == "" || id == "" {
		return Absent(AbsenceUnknown)
	}
	return store.Resolve(ctx, root, id)
}

// ListProject returns one bounded page of live project records, oldest first.
func (c recordsCatalog) ListProject(ctx context.Context, projectID string, query ArtifactPageQuery) ([]ArtifactRecord, bool, error) {
	r := c.r
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
		ArtifactScope: ArtifactScope{
			ID:              row.ID,
			ProjectID:       row.ProjectID,
			RootSessionID:   row.RootSessionID.String,
			SessionID:       row.SessionID.String,
			WorkflowRunID:   row.WorkflowRunID.String,
			ToolCallID:      row.ToolCallID,
			OriginMessageID: row.OriginMessageID,
			OperationID:     row.OperationID.String,
			NaturalKey:      row.NaturalKey.String,
		},
		ArtifactContent: ArtifactContent{
			ContentHash:    row.ContentHash,
			RetentionClass: row.RetentionClass,
			ByteSize:       row.ByteSize,
			StoredSize:     row.StoredSize,
			Mime:           row.Mime,
			Source:         row.Source,
			Caption:        row.Caption,
			Width:          int(row.Width),
			Height:         int(row.Height),
		},
		ArtifactLifecycle: ArtifactLifecycle{
			EvidenceHandle: row.EvidenceHandle,
			PageID:         row.PageID,
			Perceive:       row.Perceive != 0,
			DurationMS:     row.DurationMs,
			RecordedAt:     row.RecordedAt.String,
			CreatedAt:      row.CreatedAt,
			UpdatedAt:      row.UpdatedAt,
			DeletedAt:      row.DeletedAt.String,
			DeletedReason:  row.DeletedReason.String,
		},
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

