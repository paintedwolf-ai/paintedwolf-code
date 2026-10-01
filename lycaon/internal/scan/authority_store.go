package scan

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/db"
	scanfindings "github.com/lycaon/lycaon/internal/scan/findings"
	"github.com/lycaon/lycaon/internal/sourcesnapshot"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
	"github.com/lycaon/lycaon/pkg/api"
)

// BindPublishedSourceSnapshot replaces a warming source identity.
func (s *SQLStore) BindPublishedSourceSnapshot(ctx context.Context, id string, snapshot sourcesnapshot.Snapshot) (*api.CodeScan, error) {
	id = strings.TrimSpace(id)
	if id == "" || strings.TrimSpace(snapshot.ID) == "" || snapshot.ID == api.SourceSnapshotWarming {
		return nil, fmt.Errorf("bind source snapshot requires id and published snapshot")
	}
	err := s.inTx(ctx, func(qtx *db.Queries, tx *sql.Tx) error {
		changed, err := qtx.BindPublishedScanSnapshot(ctx, db.BindPublishedScanSnapshotParams{
			SourceSnapshotID: snapshot.ID,
			ID:               id,
		})
		if err != nil {
			return err
		}
		if changed == 0 {
			current, err := qtx.CodeScanSourceSnapshotID(ctx, id)
			if err != nil {
				return err
			}
			if current != snapshot.ID {
				return fmt.Errorf("scan %s is not awaiting a source snapshot", id)
			}
		}
		if err := qtx.BindPublishedAssessmentSnapshot(ctx, db.BindPublishedAssessmentSnapshotParams{
			SourceSnapshotID: snapshot.ID,
			ScanID:           id,
		}); err != nil {
			return err
		}
		if changed > 0 {
			if err := qtx.SetScanSourceFacts(ctx, db.SetScanSourceFactsParams{
				SourceCaptureQuality: string(snapshot.Quality),
				SourceAdmissionMode:  string(snapshot.AdmissionMode),
				ScanID:               id,
			}); err != nil {
				return err
			}
		}
		return s.emitScanTx(ctx, tx, id)
	})
	if err != nil {
		return nil, err
	}
	return s.Get(ctx, id)
}

// ErrAssessmentIdentityMismatch means an assessment id is already recorded
// over a different evaluation, such as another source generation.
var ErrAssessmentIdentityMismatch = errors.New("security assessment identity mismatch")

type AssessmentDraft struct {
	ID               string
	CanonicalPath    string
	SourceSnapshotID string
	RequiredScanners []string
	Target           TargetSelection
	Trigger          api.ScanTrigger
	CreatedAt        time.Time
}

func (s *SQLStore) EnsureAssessment(ctx context.Context, draft AssessmentDraft) (string, error) {
	if s == nil || s.db == nil {
		return "", fmt.Errorf("scan store not configured")
	}
	if strings.TrimSpace(draft.ID) == "" {
		draft.ID = uuid.NewString()
	}
	if draft.Target.Kind == "" {
		draft.Target.Kind = api.ScanTargetFull
	}
	if draft.Trigger == "" {
		draft.Trigger = api.ScanTriggerManual
	}
	if draft.CreatedAt.IsZero() {
		draft.CreatedAt = time.Now().UTC()
	}
	scanners := UniqueSortedStrings(draft.RequiredScanners)
	scannersJSON, err := surveyjson.Marshal(scanners)
	if err != nil {
		return "", err
	}
	pathsJSON, err := surveyjson.Marshal(NormalizeScanPaths(draft.Target.Paths))
	if err != nil {
		return "", err
	}
	deletedJSON, err := surveyjson.Marshal(NormalizeScanPaths(draft.Target.DeletedPaths))
	if err != nil {
		return "", err
	}
	err = s.inTx(ctx, func(qtx *db.Queries, _ *sql.Tx) error {
		if err := qtx.InsertSecurityAssessment(ctx, db.InsertSecurityAssessmentParams{
			ID: draft.ID, CanonicalPath: draft.CanonicalPath,
			SourceSnapshotID:     draft.SourceSnapshotID,
			RequiredScannersJson: string(scannersJSON),
			TargetKind:           string(draft.Target.Kind), TargetPathsJson: string(pathsJSON),
			DeletedPathsJson: string(deletedJSON), Trigger: string(draft.Trigger),
			CreatedAt: db.FormatTime(draft.CreatedAt),
		}); err != nil {
			return err
		}
		identity, err := qtx.GetSecurityAssessmentIdentity(ctx, draft.ID)
		if err != nil {
			return err
		}
		if identity.CanonicalPath != draft.CanonicalPath ||
			identity.SourceSnapshotID != draft.SourceSnapshotID ||
			identity.RequiredScannersJson != string(scannersJSON) ||
			identity.TargetKind != string(draft.Target.Kind) ||
			identity.TargetPathsJson != string(pathsJSON) ||
			identity.DeletedPathsJson != string(deletedJSON) ||
			identity.Trigger != string(draft.Trigger) {
			return fmt.Errorf("%w: %s", ErrAssessmentIdentityMismatch, draft.ID)
		}
		return nil
	})
	return draft.ID, err
}

func prepareScanFacts(scan *api.CodeScan, paths []string) error {
	if scan == nil {
		return fmt.Errorf("scan required")
	}
	if scan.AssessmentID == "" {
		scan.AssessmentID = uuid.NewString()
	}
	if scan.TargetKind == "" {
		if len(paths) == 0 {
			scan.TargetKind = api.ScanTargetFull
		} else {
			scan.TargetKind = api.ScanTargetPaths
		}
	}
	scan.TargetPaths = NormalizeScanPaths(paths)
	scan.DeletedPaths = NormalizeScanPaths(scan.DeletedPaths)
	manifest, fingerprint, scheme, err := normalizeExecutionIdentity(
		scan.ScannerID, scan.ExecutionManifest, scan.ExecutionFingerprint, scan.FingerprintScheme,
	)
	if err != nil {
		return err
	}
	scan.ExecutionManifest = manifest
	scan.ExecutionFingerprint = fingerprint
	scan.FingerprintScheme = scheme
	return nil
}

func normalizeExecutionIdentity(scannerID string, manifest *api.ScanExecutionManifest, fingerprint, scheme string) (*api.ScanExecutionManifest, string, string, error) {
	if scheme = strings.TrimSpace(scheme); scheme == "" {
		scheme = api.ScanFingerprintScheme
	}
	if manifest == nil {
		manifest = &api.ScanExecutionManifest{
			SchemaVersion: "v1", ScannerID: strings.TrimSpace(scannerID), FingerprintScheme: scheme,
		}
	}
	if fingerprint = strings.TrimSpace(fingerprint); fingerprint == "" {
		raw, err := surveyjson.Marshal(manifest)
		if err != nil {
			return nil, "", "", err
		}
		sum := sha256.Sum256(raw)
		fingerprint = hex.EncodeToString(sum[:])
	}
	return manifest, fingerprint, scheme, nil
}

type reuseIdentity struct {
	BaseSnapshotID       string             `json:"base_snapshot_id"`
	Categories           []api.ScanCategory `json:"categories"`
	TargetKind           api.ScanTargetKind `json:"target_kind"`
	TargetPaths          []string           `json:"target_paths"`
	DeletedPaths         []string           `json:"deleted_paths"`
	ExecutionFingerprint string             `json:"execution_fingerprint"`
	FingerprintScheme    string             `json:"fingerprint_scheme"`
}

func scanReuseKey(scan api.CodeScan, baseSnapshotID string) (string, error) {
	categories := scan.Categories
	// Categories select a scanner but do not distinguish its evidence.
	if strings.TrimSpace(scan.ScannerID) != "" {
		categories = nil
	}
	raw, err := surveyjson.Marshal(reuseIdentity{
		BaseSnapshotID:       baseSnapshotID,
		Categories:           categories,
		TargetKind:           scan.TargetKind,
		TargetPaths:          NormalizeScanPaths(scan.TargetPaths),
		DeletedPaths:         NormalizeScanPaths(scan.DeletedPaths),
		ExecutionFingerprint: strings.TrimSpace(scan.ExecutionFingerprint),
		FingerprintScheme:    strings.TrimSpace(scan.FingerprintScheme),
	})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func (s *SQLStore) ensureAssessmentForScan(ctx context.Context, scan api.CodeScan) error {
	identity, err := s.queries.GetSecurityAssessmentIdentity(ctx, scan.AssessmentID)
	if err == nil {
		if identity.CanonicalPath != scan.CanonicalPath ||
			identity.SourceSnapshotID != scan.SourceSnapshotID ||
			identity.TargetKind != string(scan.TargetKind) {
			return fmt.Errorf("security assessment %s does not match scan", scan.AssessmentID)
		}
		return nil
	}
	if !db.IsNoRows(err) {
		return err
	}
	_, err = s.EnsureAssessment(ctx, AssessmentDraft{
		ID: scan.AssessmentID, CanonicalPath: scan.CanonicalPath,
		SourceSnapshotID: scan.SourceSnapshotID, RequiredScanners: []string{scan.ScannerID},
		Target:  TargetSelection{Kind: scan.TargetKind, Paths: scan.TargetPaths, DeletedPaths: scan.DeletedPaths},
		Trigger: scan.Trigger, CreatedAt: scan.CreatedAt,
	})
	return err
}

func insertRunFactsTx(ctx context.Context, tx *sql.Tx, scan api.CodeScan, baseSnapshotID string) error {
	manifestJSON, err := surveyjson.Marshal(scan.ExecutionManifest)
	if err != nil {
		return err
	}
	pathsJSON, err := surveyjson.Marshal(scan.TargetPaths)
	if err != nil {
		return err
	}
	deletedJSON, err := surveyjson.Marshal(scan.DeletedPaths)
	if err != nil {
		return err
	}
	return db.New(tx).InsertScanRunFacts(ctx, db.InsertScanRunFactsParams{
		ScanID: scan.ID, AssessmentID: scan.AssessmentID, BaseSnapshotID: baseSnapshotID,
		TargetKind: string(scan.TargetKind), TargetPathsJson: string(pathsJSON),
		DeletedPathsJson: string(deletedJSON), ExecutionManifestJson: string(manifestJSON),
		ExecutionFingerprint: scan.ExecutionFingerprint, FingerprintScheme: scan.FingerprintScheme,
		SourceCaptureQuality: scan.SourceCaptureQuality, SourceAdmissionMode: scan.SourceAdmissionMode,
		CoverageStatus: string(scan.CoverageStatus), FailureCode: scan.FailureCode,
		FindingSetID: scan.FindingSetID,
	})
}

func hydrateScanFacts(ctx context.Context, handle db.DBTX, scan *api.CodeScan) error {
	if scan == nil || handle == nil {
		return nil
	}
	facts, err := db.New(handle).GetScanRunFacts(ctx, scan.ID)
	if err != nil {
		return fmt.Errorf("scan %s authority facts: %w", scan.ID, err)
	}
	scan.AssessmentID = facts.AssessmentID
	scan.ExecutionFingerprint = facts.ExecutionFingerprint
	scan.FingerprintScheme = facts.FingerprintScheme
	scan.SourceCaptureQuality = facts.SourceCaptureQuality
	scan.SourceAdmissionMode = facts.SourceAdmissionMode
	scan.FailureCode = facts.FailureCode
	scan.FindingSetID = facts.FindingSetID
	scan.TargetKind = api.ScanTargetKind(facts.TargetKind)
	scan.CoverageStatus = api.ScanCoverageStatus(facts.CoverageStatus)
	if err := json.Unmarshal([]byte(facts.TargetPathsJson), &scan.TargetPaths); err != nil {
		return err
	}
	if err := json.Unmarshal([]byte(facts.DeletedPathsJson), &scan.DeletedPaths); err != nil {
		return err
	}
	var manifest api.ScanExecutionManifest
	if err := json.Unmarshal([]byte(facts.ExecutionManifestJson), &manifest); err != nil {
		return err
	}
	scan.ExecutionManifest = &manifest
	if scan.Warnings == nil {
		scan.Warnings = WarningsFromResult(scan.Result)
	}
	return nil
}

func (s *SQLStore) hydrateScan(ctx context.Context, scan *api.CodeScan) (*api.CodeScan, error) {
	if scan != nil {
		pruned, err := db.New(s.db).GetHistoryBodyPrunedAt(ctx, db.GetHistoryBodyPrunedAtParams{Class: "scan_detail", OwnerID: scan.ID})
		if err != nil && !db.IsNoRows(err) {
			return nil, err
		}
		if pruned != "" {
			scan.DetailPrunedAt = pruned
			return scan, nil
		}
	}
	if err := hydrateScanFacts(ctx, s.db, scan); err != nil {
		return nil, err
	}
	if scan != nil && scan.FindingSetID != "" {
		findings, coverage, err := s.findingSetForScan(ctx, scan.ID)
		if err != nil {
			return nil, err
		}
		if err := s.attachFindingHistory(ctx, scan.CanonicalPath, scan.ScannerID, findings); err != nil {
			return nil, err
		}
		scan.Findings = findings
		scan.FindingsStored = len(findings)
		scan.FindingsByLevel = scanfindings.CountFindingsByLevel(findings)
		scan.CoverageStatus = coverage
	}
	return scan, nil
}

func (s *SQLStore) hydrateScans(ctx context.Context, scans []api.CodeScan) ([]api.CodeScan, error) {
	for index := range scans {
		if _, err := s.hydrateScan(ctx, &scans[index]); err != nil {
			return nil, err
		}
	}
	return scans, nil
}

// Comparisons and incremental bases require coverage of every admitted file.
func CoversGeneration(status api.ScanCoverageStatus) bool {
	return status == api.ScanCoverageComplete || status == api.ScanCoverageBounded
}

// EstablishesAuthority reports whether a scan completed with generation coverage.
func EstablishesAuthority(scan api.CodeScan) bool {
	return scan.Status == api.CodeScanStatusComplete && CoversGeneration(scan.CoverageStatus)
}

// FinalizeCompleteJSON atomically commits completion and its finding set.
func (s *SQLStore) FinalizeCompleteJSON(ctx context.Context, claimed *api.CodeScan, resultJSON []byte, findings []api.SecurityFinding, warnings []api.ScanWarning, coverage api.ScanCoverageStatus) (bool, error) {
	if claimed == nil || claimed.ID == "" || claimed.ClaimToken == "" {
		return false, fmt.Errorf("scan claim token required")
	}
	if !json.Valid(resultJSON) {
		return false, fmt.Errorf("scan result must be valid JSON")
	}
	won := false
	err := s.inTx(ctx, func(qtx *db.Queries, tx *sql.Tx) error {
		changed, err := qtx.MarkScanComplete(ctx, db.MarkScanCompleteParams{
			ResultJson:  db.NullString(string(resultJSON)),
			CompletedAt: db.NullString(db.FormatTime(time.Now().UTC())),
			ID:          claimed.ID, ClaimToken: db.NullString(claimed.ClaimToken),
		})
		if err != nil {
			return err
		}
		if changed != 1 {
			return nil
		}
		setID, reduced, finalCoverage, err := commitFindingSetTx(ctx, tx, claimed, findings, warnings, coverage)
		if err != nil {
			return err
		}
		claimed.FindingSetID = setID
		claimed.Findings = reduced
		claimed.FindingsStored = len(reduced)
		claimed.CoverageStatus = finalCoverage
		won = true
		return s.emitScanTx(ctx, tx, claimed.ID)
	})
	return won && err == nil, err
}

// FinalizeFailure atomically commits failure and unavailable coverage.
func (s *SQLStore) FinalizeFailure(ctx context.Context, claimed *api.CodeScan, status api.CodeScanStatus, code, message string) (bool, error) {
	if claimed == nil || claimed.ID == "" || claimed.ClaimToken == "" {
		return false, fmt.Errorf("scan claim token required")
	}
	switch status {
	case api.CodeScanStatusFailed, api.CodeScanStatusTimedOut, api.CodeScanStatusCanceled:
	default:
		return false, fmt.Errorf("invalid scan failure status %q", status)
	}
	won := false
	err := s.inTx(ctx, func(qtx *db.Queries, tx *sql.Tx) error {
		changed, err := qtx.MarkScanTerminalFailure(ctx, db.MarkScanTerminalFailureParams{
			Status: string(status), Error: db.NullString(strings.TrimSpace(message)),
			CompletedAt: db.NullString(db.FormatTime(time.Now().UTC())),
			ID:          claimed.ID, ClaimToken: db.NullString(claimed.ClaimToken),
		})
		if err != nil {
			return err
		}
		if changed != 1 {
			return nil
		}
		if err := qtx.SetScanRunFailure(ctx, db.SetScanRunFailureParams{
			CoverageStatus: string(api.ScanCoverageUnavailable),
			FailureCode:    strings.TrimSpace(code), ScanID: claimed.ID,
		}); err != nil {
			return err
		}
		won = true
		return s.emitScanTx(ctx, tx, claimed.ID)
	})
	return won && err == nil, err
}

// FinalizePendingFailure records a typed failure before a job is claimed.
func (s *SQLStore) FinalizePendingFailure(ctx context.Context, id, code, message string) (bool, error) {
	id = strings.TrimSpace(id)
	code = strings.TrimSpace(code)
	if id == "" || code == "" {
		return false, fmt.Errorf("scan id and failure code required")
	}
	won := false
	err := s.inTx(ctx, func(qtx *db.Queries, tx *sql.Tx) error {
		changed, err := qtx.MarkPendingScanFailed(ctx, db.MarkPendingScanFailedParams{
			Error:       db.NullString(strings.TrimSpace(message)),
			CompletedAt: db.NullString(db.FormatTime(time.Now().UTC())), ID: id,
		})
		if err != nil || changed != 1 {
			return err
		}
		if err := qtx.SetScanRunFailure(ctx, db.SetScanRunFailureParams{
			CoverageStatus: string(api.ScanCoverageUnavailable), FailureCode: code, ScanID: id,
		}); err != nil {
			return err
		}
		won = true
		return s.emitScanTx(ctx, tx, id)
	})
	return won && err == nil, err
}

func commitFindingSetTx(ctx context.Context, tx *sql.Tx, scan *api.CodeScan, findings []api.SecurityFinding, warnings []api.ScanWarning, coverage api.ScanCoverageStatus) (string, []api.SecurityFinding, api.ScanCoverageStatus, error) {
	qtx := db.New(tx)
	setID := uuid.NewString()
	findings = append([]api.SecurityFinding(nil), findings...)
	warnings = append([]api.ScanWarning(nil), warnings...)
	baseID := ""
	var err error
	if scan.TargetKind == api.ScanTargetPaths {
		base, baseErr := qtx.LatestCoveringFindingSetBase(ctx, db.LatestCoveringFindingSetBaseParams{
			ScanID:        scan.ID,
			CanonicalPath: scan.CanonicalPath, ScannerID: scan.ScannerID,
			ExecutionFingerprint: scan.ExecutionFingerprint,
			FingerprintScheme:    scan.FingerprintScheme,
		})
		err = baseErr
		switch {
		case err == nil:
			baseID = base
			baseFindings, err := loadFindingEntries(ctx, qtx, base)
			if err != nil {
				return "", nil, coverage, err
			}
			findings = reduceIncrementalFindings(baseFindings, findings, scan.TargetPaths, scan.DeletedPaths)
		case db.IsNoRows(err):
			baseID = ""
			coverage = api.ScanCoveragePartial
		default:
			return "", nil, coverage, err
		}
	}
	SortFindingsByIdentity(findings)
	warningsJSON, err := surveyjson.Marshal(warnings)
	if err != nil {
		return "", nil, coverage, err
	}
	if err := qtx.InsertScanFindingSet(ctx, db.InsertScanFindingSetParams{
		ID: setID, ScanID: scan.ID, BaseSetID: db.NullString(baseID),
		CanonicalPath: scan.CanonicalPath, ScannerID: scan.ScannerID,
		SourceSnapshotID:     scan.SourceSnapshotID,
		ExecutionFingerprint: scan.ExecutionFingerprint,
		FingerprintScheme:    scan.FingerprintScheme, CoverageStatus: string(coverage),
		WarningsJson: string(warningsJSON),
		CreatedAt:    db.FormatTime(time.Now().UTC()),
	}); err != nil {
		return "", nil, coverage, err
	}
	for ordinal, finding := range findings {
		raw, err := surveyjson.Marshal(finding)
		if err != nil {
			return "", nil, coverage, err
		}
		if err := qtx.InsertFindingEntry(ctx, db.InsertFindingEntryParams{FindingSetID: setID, Ordinal: int64(ordinal), FindingJson: string(raw)}); err != nil {
			return "", nil, coverage, err
		}
	}
	rollupJSON, err := surveyjson.Marshal(rollupFindings(findings))
	if err != nil {
		return "", nil, coverage, err
	}
	if err := qtx.PutFindingRollup(ctx, db.PutFindingRollupParams{FindingSetID: setID, RollupJson: string(rollupJSON)}); err != nil {
		return "", nil, coverage, err
	}
	changed, err := qtx.BindScanFindingSet(ctx, db.BindScanFindingSetParams{
		FindingSetID: setID, CoverageStatus: string(coverage), ScanID: scan.ID,
	})
	if err != nil {
		return "", nil, coverage, err
	}
	if changed != 1 {
		return "", nil, coverage, fmt.Errorf("scan %s has no durable run facts", scan.ID)
	}
	return setID, findings, coverage, nil
}

func reduceIncrementalFindings(base, changed []api.SecurityFinding, targetPaths, deletedPaths []string) []api.SecurityFinding {
	replaced := append(NormalizeScanPaths(targetPaths), NormalizeScanPaths(deletedPaths)...)
	out := make([]api.SecurityFinding, 0, len(base)+len(changed))
	for _, finding := range base {
		if FindingTouchesPaths(finding, replaced) {
			continue
		}
		out = append(out, finding)
	}
	return append(out, changed...)
}

func FindingTouchesPaths(finding api.SecurityFinding, paths []string) bool {
	for _, location := range finding.Locations {
		uri := strings.Trim(filepath.ToSlash(location.URI), "/")
		for _, raw := range paths {
			path := strings.Trim(filepath.ToSlash(raw), "/")
			if uri == path || strings.HasPrefix(uri, path+"/") {
				return true
			}
		}
	}
	return false
}

func (s *SQLStore) findingSetForScan(ctx context.Context, scanID string) ([]api.SecurityFinding, api.ScanCoverageStatus, error) {
	row, err := s.queries.GetScanFindingSetForScan(ctx, scanID)
	if err != nil {
		return nil, "", fmt.Errorf("scan %s finding set: %w", scanID, err)
	}
	findings, err := loadFindingEntries(ctx, s.queries, row.ID)
	if err != nil {
		return nil, "", err
	}
	return findings, api.ScanCoverageStatus(row.CoverageStatus), nil
}

func UniqueSortedStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}
