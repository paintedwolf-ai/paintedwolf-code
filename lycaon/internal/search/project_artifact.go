package search

import (
	"context"
	"database/sql"
	"strings"
)

const artifactShape = "artifact"

// Artifact search rows use the content hash as their handle.
type ProjectArtifactInput struct {
	ID             string
	Hash           string
	Mime           string
	Source         string
	Caption        string
	EvidenceHandle string
	SessionID      string
	WorkflowRunID  string
	ToolCallID     string
	CreatedAt      string
}

// ProjectArtifact projects one durable visual artifact into an evidence_index row.
// Kind is the producer source (render|capture|user). CheckID holds evidence_handle.
// Artifact identity keeps updates on the same projection row.
func ProjectArtifact(projectID string, row ProjectArtifactInput) []IndexRow {
	projectID = strings.TrimSpace(projectID)
	id := strings.TrimSpace(row.ID)
	hash := strings.TrimSpace(row.Hash)
	if projectID == "" || id == "" || hash == "" {
		return nil
	}
	return []IndexRow{{
		ID:        ArtifactRowID(id),
		ProjectID: projectID,
		Source:    SourceArtifact,
		HitKind:   HitKindArtifact,
		SessionID: strings.TrimSpace(row.SessionID),
		SourceRef: id,
		Handle:    hash,
		Kind:      strings.TrimSpace(row.Source),
		Shape:     artifactShape,
		Snippet:   strings.TrimSpace(row.Caption),
		CheckID:   strings.TrimSpace(row.EvidenceHandle),
		Role:      strings.TrimSpace(row.ToolCallID),
		LegID:     strings.TrimSpace(row.WorkflowRunID),
		Path:      strings.TrimSpace(row.Mime),
		TS:        strings.TrimSpace(row.CreatedAt),
	}}
}

// ArtifactRowID is the evidence_index primary key for one artifact id.
func ArtifactRowID(artifactID string) string {
	return RowID(SourceArtifact, strings.TrimSpace(artifactID), "")
}

// ProjectArtifactTx commits the projection in the artifact transaction.
func ProjectArtifactTx(ctx context.Context, tx *sql.Tx, projectID string, row ProjectArtifactInput) error {
	if tx == nil {
		return nil
	}
	rows := ProjectArtifact(projectID, row)
	if len(rows) == 0 {
		return nil
	}
	return defaultStore.UpsertRows(ctx, tx, rows)
}

// DeleteArtifactProjectionTx drops one artifact's evidence_index row.
func DeleteArtifactProjectionTx(ctx context.Context, tx *sql.Tx, artifactID string) error {
	if tx == nil || strings.TrimSpace(artifactID) == "" {
		return nil
	}
	_, err := tx.ExecContext(ctx, `DELETE FROM evidence_index WHERE id = ?`, ArtifactRowID(artifactID))
	return err
}
