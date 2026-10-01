package scan

import (
	"context"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/pkg/api"
)

// Get returns a scan by id, or (nil, nil) when no row exists.
func (s *SQLStore) Get(ctx context.Context, id string) (*api.CodeScan, error) {
	row, err := s.queries.GetCodeScan(ctx, id)
	if db.IsNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return s.hydrateScan(ctx, codeScanFromRow(row))
}

// FindReusableByPathScanner returns matching active or complete evidence.
func (s *SQLStore) FindReusableByPathScanner(ctx context.Context, canonicalPath, snapshotID, scannerID, reuseKey string) (*api.CodeScan, error) {
	canonicalPath = strings.TrimSpace(canonicalPath)
	scannerID = strings.TrimSpace(scannerID)
	snapshotID = strings.TrimSpace(snapshotID)
	if canonicalPath == "" || scannerID == "" {
		return nil, nil
	}
	row, err := s.queries.FindReusableScanByPathScanner(ctx, db.FindReusableScanByPathScannerParams{
		CanonicalPath:    canonicalPath,
		ScannerID:        db.NullString(scannerID),
		SourceSnapshotID: snapshotID,
		ReuseKey:         strings.TrimSpace(reuseKey),
	})
	if db.IsNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return s.hydrateScan(ctx, codeScanFromRow(row))
}

// OpenScansForPath returns pending or running scans for a path across all triggers.
func (s *SQLStore) OpenScansForPath(ctx context.Context, canonicalPath string) ([]api.CodeScan, error) {
	rows, err := s.queries.OpenScansForPath(ctx, canonicalPath)
	if err != nil {
		return nil, err
	}
	return s.hydrateScans(ctx, codeScansFromRows(rows))
}

// ListOpen returns every pending or running scan.
func (s *SQLStore) ListOpen(ctx context.Context) ([]api.CodeScan, error) {
	rows, err := s.queries.ListOpenScans(ctx)
	if err != nil {
		return nil, err
	}
	return s.hydrateScans(ctx, codeScansFromRows(rows))
}

// ListByCanonicalPath returns scans for one canonical path.
func (s *SQLStore) ListByCanonicalPath(ctx context.Context, canonicalPath string) ([]api.CodeScan, error) {
	rows, err := s.queries.ListScansByCanonicalPath(ctx, canonicalPath)
	if err != nil {
		return nil, err
	}
	return s.hydrateScans(ctx, codeScansFromRows(rows))
}

// ListByWorkflowRunID returns scans bound to a workflow run, newest first.
func (s *SQLStore) ListByWorkflowRunID(ctx context.Context, workflowRunID string) ([]api.CodeScan, error) {
	workflowRunID = strings.TrimSpace(workflowRunID)
	if workflowRunID == "" {
		return nil, nil
	}
	rows, err := s.queries.ListScansByWorkflowRunID(ctx, workflowRunID)
	if err != nil {
		return nil, err
	}
	scans, err := s.hydrateScans(ctx, codeScansFromRows(rows))
	for i := range scans {
		scans[i].WorkflowRunID = workflowRunID
	}
	return scans, err
}

// ListBySessionID returns scans the session asked for, newest first.
func (s *SQLStore) ListBySessionID(ctx context.Context, sessionID string) ([]api.CodeScan, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, nil
	}
	rows, err := s.queries.ListScansBySessionID(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	scans, err := s.hydrateScans(ctx, codeScansFromRows(rows))
	for i := range scans {
		scans[i].SessionID = sessionID
	}
	return scans, err
}

// LatestForDelegation returns the newest scan for a delegation and category set.
func (s *SQLStore) LatestForDelegation(ctx context.Context, delegationID string, categories []api.ScanCategory) (*api.CodeScan, error) {
	if delegationID == "" {
		return nil, fmt.Errorf("delegation_id required")
	}
	categoriesJSON, err := categoriesJSON(categories)
	if err != nil {
		return nil, err
	}
	row, err := s.queries.LatestScanForDelegation(ctx, db.LatestScanForDelegationParams{
		DelegationID:   delegationID,
		CategoriesJson: categoriesJSON,
	})
	if db.IsNoRows(err) {
		return nil, fmt.Errorf("code scan not found for delegation %s", delegationID)
	}
	if err != nil {
		return nil, err
	}
	return s.hydrateScan(ctx, codeScanFromRow(row))
}

// LatestCompleteForDelegationSnapshot returns the newest complete scan for a delegation snapshot.
func (s *SQLStore) LatestCompleteForDelegationSnapshot(ctx context.Context, delegationID, snapshotID string, categories []api.ScanCategory) (*api.CodeScan, error) {
	if delegationID == "" {
		return nil, fmt.Errorf("delegation_id required")
	}
	categoriesJSON, err := categoriesJSON(categories)
	if err != nil {
		return nil, err
	}
	row, err := s.queries.LatestCompleteScanForDelegationSnapshot(ctx, db.LatestCompleteScanForDelegationSnapshotParams{
		DelegationID:     delegationID,
		CategoriesJson:   categoriesJSON,
		SourceSnapshotID: strings.TrimSpace(snapshotID),
	})
	if db.IsNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return s.hydrateScan(ctx, codeScanFromRow(row))
}

// FindInFlightForDelegationSnapshot returns a matching pending or running scan.
func (s *SQLStore) FindInFlightForDelegationSnapshot(ctx context.Context, delegationID, snapshotID string, categories []api.ScanCategory, paths []string) (*api.CodeScan, error) {
	if delegationID == "" {
		return nil, nil
	}
	categoriesJSON, err := categoriesJSON(categories)
	if err != nil {
		return nil, err
	}
	// Paths are part of the delegation snapshot identity.
	pathsJSON, err := db.MarshalJSON(NormalizeScanPaths(paths))
	if err != nil {
		return nil, err
	}
	row, err := s.queries.FindInFlightScanForDelegationSnapshot(ctx, db.FindInFlightScanForDelegationSnapshotParams{
		DelegationID:     delegationID,
		CategoriesJson:   categoriesJSON,
		SourceSnapshotID: strings.TrimSpace(snapshotID),
		PathsJson:        pathsJSON.String,
	})
	if db.IsNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return s.hydrateScan(ctx, codeScanFromRow(row))
}
