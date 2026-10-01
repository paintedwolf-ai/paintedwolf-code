package search

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/timelayout"
	"github.com/lycaon/lycaon/pkg/api"
)

// ProjectScanComplete projects ingest findings for one completed scan.
func ProjectScanComplete(ctx context.Context, sqlDB db.Handle, scanID string) error {
	scanID = strings.TrimSpace(scanID)
	if scanID == "" {
		return nil
	}
	var delegationID, canonicalPath, completedAt, ingestJSON sql.NullString
	err := sqlDB.QueryRowContext(ctx, `
		SELECT delegation_id, canonical_path, completed_at, ingest_json
		FROM code_scans WHERE id = ?
	`, scanID).Scan(&delegationID, &canonicalPath, &completedAt, &ingestJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if !ingestJSON.Valid || strings.TrimSpace(ingestJSON.String) == "" {
		return nil
	}
	projectID, err := resolveScanProjectID(ctx, sqlDB, delegationID.String, canonicalPath.String)
	if err != nil {
		return err
	}
	if projectID == "" {
		return nil
	}
	findings, err := parseIngestFindings(ingestJSON.String)
	if err != nil {
		return err
	}
	ts := strings.TrimSpace(completedAt.String)
	if ts == "" {
		ts = timelayout.Format(time.Now().UTC())
	}
	rows := ProjectScanFindings(projectID, scanID, findings, ts)
	tx, err := sqlDB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := defaultStore.UpsertRows(ctx, tx, rows); err != nil {
		return err
	}
	return tx.Commit()
}

func resolveScanProjectID(ctx context.Context, sqlDB db.Handle, delegationID, canonicalPath string) (string, error) {
	delegationID = strings.TrimSpace(delegationID)
	if delegationID != "" {
		var projectID string
		err := sqlDB.QueryRowContext(ctx, `
			SELECT project_id FROM delegations WHERE id = ?
		`, delegationID).Scan(&projectID)
		if err == nil {
			return strings.TrimSpace(projectID), nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return "", err
		}
	}
	canonicalPath = strings.TrimSpace(canonicalPath)
	if canonicalPath == "" {
		return "", nil
	}
	var projectID string
	err := sqlDB.QueryRowContext(ctx, `
		SELECT project_id FROM project_roots WHERE path = ? LIMIT 1
	`, canonicalPath).Scan(&projectID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(projectID), nil
}

func parseIngestFindings(raw string) ([]api.SecurityFinding, error) {
	var payload struct {
		Findings []api.SecurityFinding `json:"findings"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return nil, fmt.Errorf("parse scan ingest_json: %w", err)
	}
	return payload.Findings, nil
}
