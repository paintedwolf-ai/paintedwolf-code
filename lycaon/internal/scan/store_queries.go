package scan

import (
	"context"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/pkg/api"
)

// Get returns a scan by id, or (nil, nil) when no row exists.
func (s *ReadStore) Get(ctx context.Context, id string) (*api.CodeScan, error) {
	row, err := s.tx.queries.GetCodeScan(ctx, id)
	if db.IsNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return s.hydrateScan(ctx, codeScanFromRow(row))
}

// FindReusableByPathScanner returns matching active or complete evidence.
func (s *ReadStore) FindReusableByPathScanner(ctx context.Context, canonicalPath, snapshotID, scannerID, reuseKey string) (*api.CodeScan, error) {
	canonicalPath = strings.TrimSpace(canonicalPath)
	scannerID = strings.TrimSpace(scannerID)
	snapshotID = strings.TrimSpace(snapshotID)
	if canonicalPath == "" || scannerID == "" {
		return nil, nil
	}
	row, err := s.tx.queries.FindReusableScanByPathScanner(ctx, db.FindReusableScanByPathScannerParams{
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
func (s *ReadStore) OpenScansForPath(ctx context.Context, canonicalPath string) ([]api.CodeScan, error) {
	rows, err := s.tx.queries.OpenScansForPath(ctx, canonicalPath)
	if err != nil {
		return nil, err
	}
	return s.hydrateScans(ctx, codeScansFromRows(rows))
}

// ListOpen returns every pending or running scan.
func (s *ReadStore) ListOpen(ctx context.Context) ([]api.CodeScan, error) {
	rows, err := s.tx.queries.ListOpenScans(ctx)
	if err != nil {
		return nil, err
	}
	return s.hydrateScans(ctx, codeScansFromRows(rows))
}

// ListByCanonicalPath returns scans for one canonical path.
func (s *ReadStore) ListByCanonicalPath(ctx context.Context, canonicalPath string) ([]api.CodeScan, error) {
	rows, err := s.tx.queries.ListScansByCanonicalPath(ctx, canonicalPath)
	if err != nil {
		return nil, err
	}
	return s.hydrateScans(ctx, codeScansFromRows(rows))
}

// ListByWorkflowRunID returns scans bound to a workflow run, newest first.
func (s *ReadStore) ListByWorkflowRunID(ctx context.Context, workflowRunID string) ([]api.CodeScan, error) {
	workflowRunID = strings.TrimSpace(workflowRunID)
	if workflowRunID == "" {
		return nil, nil
	}
	rows, err := s.tx.queries.ListScansByWorkflowRunID(ctx, workflowRunID)
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
func (s *ReadStore) ListBySessionID(ctx context.Context, sessionID string) ([]api.CodeScan, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, nil
	}
	rows, err := s.tx.queries.ListScansBySessionID(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	scans, err := s.hydrateScans(ctx, codeScansFromRows(rows))
	for i := range scans {
		scans[i].SessionID = sessionID
	}
	return scans, err
}

// LatestCompleteForSessionSnapshot returns the newest complete scan bound to sessionID for a snapshot.
func (s *ReadStore) LatestCompleteForSessionSnapshot(ctx context.Context, sessionID, snapshotID string, categories []api.ScanCategory) (*api.CodeScan, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, fmt.Errorf("session_id required")
	}
	categoriesJSON, err := categoriesJSON(categories)
	if err != nil {
		return nil, err
	}
	row, err := s.tx.queries.LatestCompleteScanForSessionSnapshot(ctx, db.LatestCompleteScanForSessionSnapshotParams{
		SessionID:        sessionID,
		CategoriesJson:   categoriesJSON,
		SourceSnapshotID: strings.TrimSpace(snapshotID),
	})
	if db.IsNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	scan, err := s.hydrateScan(ctx, codeScanFromRow(row))
	if scan != nil {
		scan.SessionID = sessionID
	}
	return scan, err
}

// FindInFlightForSessionSnapshot returns a matching pending or running scan bound to sessionID.
func (s *ReadStore) FindInFlightForSessionSnapshot(ctx context.Context, sessionID, snapshotID string, categories []api.ScanCategory, paths []string) (*api.CodeScan, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, nil
	}
	categoriesJSON, err := categoriesJSON(categories)
	if err != nil {
		return nil, err
	}
	pathsJSON, err := db.MarshalJSON(NormalizeScanPaths(paths))
	if err != nil {
		return nil, err
	}
	row, err := s.tx.queries.FindInFlightScanForSessionSnapshot(ctx, db.FindInFlightScanForSessionSnapshotParams{
		SessionID:        sessionID,
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
	scan, err := s.hydrateScan(ctx, codeScanFromRow(row))
	if scan != nil {
		scan.SessionID = sessionID
	}
	return scan, err
}

// LatestCompleteForDelegationSnapshot returns the newest complete scan for a delegation snapshot.
func (s *ReadStore) LatestCompleteForDelegationSnapshot(ctx context.Context, delegationID, snapshotID string, categories []api.ScanCategory) (*api.CodeScan, error) {
	if delegationID == "" {
		return nil, fmt.Errorf("delegation_id required")
	}
	categoriesJSON, err := categoriesJSON(categories)
	if err != nil {
		return nil, err
	}
	row, err := s.tx.queries.LatestCompleteScanForDelegationSnapshot(ctx, db.LatestCompleteScanForDelegationSnapshotParams{
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
func (s *ReadStore) FindInFlightForDelegationSnapshot(ctx context.Context, delegationID, snapshotID string, categories []api.ScanCategory, paths []string) (*api.CodeScan, error) {
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
	row, err := s.tx.queries.FindInFlightScanForDelegationSnapshot(ctx, db.FindInFlightScanForDelegationSnapshotParams{
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
