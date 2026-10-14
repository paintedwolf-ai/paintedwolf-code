package visual

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/pkg/api"
)

type deletionGuardKey struct{}

// WithDeletionGuard rechecks host-owned eligibility in the deletion transaction.
func WithDeletionGuard(ctx context.Context, guard func(context.Context, *sql.Tx) error) context.Context {
	return context.WithValue(ctx, deletionGuardKey{}, guard)
}

func guardDeletion(ctx context.Context, tx *sql.Tx) error {
	if guard, ok := ctx.Value(deletionGuardKey{}).(func(context.Context, *sql.Tx) error); ok {
		return guard(ctx, tx)
	}
	return nil
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

