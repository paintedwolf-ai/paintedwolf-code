package project

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/pkg/api"
)

// MarkTrustSeen commits the captured baseline only against the project that was read.
func (r *SQLRegistry) MarkTrustSeen(ctx context.Context, id string, expected *Project, seen map[string]SeenRecord) (*Project, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	q := r.queries.WithTx(tx)
	row, err := q.GetProjectByID(ctx, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, id)
		}
		return nil, err
	}
	current, err := decodeTrustSeen(row.TrustReadBaseline)
	if err != nil {
		return nil, err
	}
	if expected == nil || int(row.RootsGeneration) != expected.RootsGeneration || !sameSeenRecords(current, expected.TrustSeen) {
		return nil, ErrTrustReviewChanged
	}
	current = cloneTrustReadBaseline(seen)
	snapshot, err := json.Marshal(current)
	if err != nil {
		return nil, fmt.Errorf("encode trust baseline: %w", err)
	}
	if err := q.SetProjectTrustBaseline(ctx, db.SetProjectTrustBaselineParams{ProjectID: id, ReviewState: string(snapshot)}); err != nil {
		return nil, err
	}
	encoded, err := encodeTrustSeen(current)
	if err != nil {
		return nil, err
	}
	if err := q.SetProjectTrustSeen(ctx, db.SetProjectTrustSeenParams{
		TrustReadBaseline: encoded,
		ID:                id,
	}); err != nil {
		return nil, err
	}
	after, err := loadProject(ctx, q, id)
	if err != nil {
		return nil, err
	}
	if err := r.enqueueProjectEventTx(ctx, tx, api.ProjectEventUpdated, after); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	r.notifyProjectEvents()
	return after, nil
}

func (r *SQLRegistry) SetTrustEnabled(ctx context.Context, id string, updates map[string]bool) (*Project, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	q := r.queries.WithTx(tx)
	row, err := q.GetProjectByID(ctx, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, id)
		}
		return nil, err
	}
	current, err := decodeTrustEnabled(row.TrustEnabled)
	if err != nil {
		return nil, err
	}
	merged := MergeTrustEnabled(current, updates)
	encoded, err := encodeTrustEnabled(merged)
	if err != nil {
		return nil, err
	}
	if err := q.SetProjectTrustEnabled(ctx, db.SetProjectTrustEnabledParams{
		TrustEnabled: encoded,
		ID:           id,
	}); err != nil {
		return nil, err
	}
	after, err := loadProject(ctx, q, id)
	if err != nil {
		return nil, err
	}
	if err := r.enqueueProjectEventTx(ctx, tx, api.ProjectEventUpdated, after); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	r.notifyProjectEvents()
	return after, nil
}

func decodeTrustEnabled(raw string) (map[string]bool, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "{}" {
		return nil, nil
	}
	var out map[string]bool
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, fmt.Errorf("decode trust_enabled: %w", err)
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

func encodeTrustEnabled(m map[string]bool) (string, error) {
	if len(m) == 0 {
		return "{}", nil
	}
	data, err := json.Marshal(m)
	if err != nil {
		return "", fmt.Errorf("encode trust_enabled: %w", err)
	}
	return string(data), nil
}

func decodeTrustSeen(raw string) (map[string]SeenRecord, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "{}" {
		return nil, nil
	}
	var out map[string]SeenRecord
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, fmt.Errorf("decode trust read baseline: %w", err)
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

func encodeTrustSeen(m map[string]SeenRecord) (string, error) {
	if len(m) == 0 {
		return "{}", nil
	}
	data, err := json.Marshal(seenSummaries(m))
	if err != nil {
		return "", fmt.Errorf("encode trust read baseline: %w", err)
	}
	return string(data), nil
}

func (r *SQLRegistry) ReadTrustBaseline(ctx context.Context, id string) (map[string]SeenRecord, error) {
	raw, err := r.queries.GetProjectTrustBaseline(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return decodeTrustSeen(raw)
}
