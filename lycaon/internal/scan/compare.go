package scan

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/db"
	scancfg "github.com/lycaon/lycaon/internal/scan/configuration"
	"github.com/lycaon/lycaon/internal/sourcesnapshot"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
	"github.com/lycaon/lycaon/pkg/api"
)

// Compare reject codes returned by scan_compare validation.
const (
	CompareRejectOldNotFound        = "SCAN_COMPARE_OLD_NOT_FOUND"
	CompareRejectNewNotFound        = "SCAN_COMPARE_NEW_NOT_FOUND"
	CompareRejectNotComplete        = "SCAN_COMPARE_NOT_COMPLETE"
	CompareRejectProjectMismatch    = "SCAN_COMPARE_PROJECT_MISMATCH"
	CompareRejectSameScan           = "SCAN_COMPARE_SAME_SCAN"
	CompareRejectScannerMismatch    = "SCAN_COMPARE_SCANNER_MISMATCH"
	CompareRejectDefinitionMismatch = "SCAN_COMPARE_DEFINITION_MISMATCH"
	CompareRejectFingerprintScheme  = "SCAN_COMPARE_FINGERPRINT_SCHEME_MISMATCH"
	CompareRejectCoverageIncomplete = "SCAN_COMPARE_COVERAGE_INCOMPLETE"
)

// CompareReject is a structured scan_compare validation failure, formatted for tools.
type CompareReject struct {
	Code string
	Data map[string]any
}

func (e *CompareReject) Error() string {
	if e == nil {
		return ""
	}
	return e.Code
}

// An omitted baseline selects the preceding compatible complete scan.
func ResolveCompareBaseline(ctx context.Context, coord ScanCoordinator, oldScanID, newScanID string) (string, error) {
	if coord == nil {
		return "", fmt.Errorf("scan coordinator not configured")
	}
	oldScanID = strings.TrimSpace(oldScanID)
	newScanID = strings.TrimSpace(newScanID)
	if newScanID == "" {
		return "", fmt.Errorf("new_scan_id is required")
	}
	if oldScanID != "" {
		return oldScanID, nil
	}
	latest, err := coord.Get(ctx, newScanID)
	if err != nil {
		return "", err
	}
	if latest == nil {
		return "", &CompareReject{Code: CompareRejectNewNotFound}
	}
	baseline, err := coord.PreviousComplete(ctx, *latest)
	if err != nil {
		return "", err
	}
	if baseline == nil || strings.TrimSpace(baseline.ID) == "" {
		return "", &CompareReject{
			Code: CompareRejectOldNotFound,
			Data: map[string]any{"reason": "no_previous_complete", "scan_baseline_missing": true},
		}
	}
	return baseline.ID, nil
}

// Compare returns finding-set differences between compatible complete scans.
func (c *CoordinatorImpl) Compare(ctx context.Context, oldScanID, newScanID string) (*Comparison, error) {
	if c == nil || c.Store == nil {
		return nil, fmt.Errorf("scan coordinator not configured")
	}
	oldScanID = strings.TrimSpace(oldScanID)
	newScanID = strings.TrimSpace(newScanID)
	if oldScanID == "" || newScanID == "" {
		return nil, fmt.Errorf("old_scan_id and new_scan_id required")
	}
	if oldScanID == newScanID {
		return nil, &CompareReject{Code: CompareRejectSameScan}
	}

	metadata, err := c.Store.metadata(ctx, []string{oldScanID, newScanID})
	if err != nil {
		return nil, err
	}
	var oldScan, newScan *api.CodeScan
	for i := range metadata {
		if metadata[i].ID == oldScanID {
			oldScan = &metadata[i]
		}
		if metadata[i].ID == newScanID {
			newScan = &metadata[i]
		}
	}
	if oldScan == nil {
		return nil, &CompareReject{Code: CompareRejectOldNotFound}
	}
	if newScan == nil {
		return nil, &CompareReject{Code: CompareRejectNewNotFound}
	}
	if err := validateComparison(oldScan, newScan); err != nil {
		return nil, err
	}
	if err := c.validateBoundedComparison(ctx, oldScan, newScan); err != nil {
		return nil, err
	}
	if raw, err := c.Store.queries.GetScanComparison(ctx, db.GetScanComparisonParams{OldSetID: oldScan.FindingSetID, NewSetID: newScan.FindingSetID}); err == nil {
		var cached Comparison
		err := json.Unmarshal([]byte(raw), &cached)
		return &cached, err
	} else if !db.IsNoRows(err) {
		return nil, err
	}
	var response *Comparison
	err = c.Store.inTx(ctx, func(q *db.Queries, _ *sql.Tx) error {
		var err error
		response, err = comparisonProjection(ctx, q, oldScan, newScan)
		return err
	})
	return response, err
}

func validateComparison(oldScan, newScan *api.CodeScan) error {
	if oldScan.Status != api.CodeScanStatusComplete || newScan.Status != api.CodeScanStatusComplete {
		return &CompareReject{Code: CompareRejectNotComplete, Data: map[string]any{"scan_old_status": string(oldScan.Status), "scan_new_status": string(newScan.Status)}}
	}
	if oldScan.CanonicalPath != newScan.CanonicalPath {
		return &CompareReject{Code: CompareRejectProjectMismatch}
	}
	if oldScan.ScannerID != newScan.ScannerID {
		return &CompareReject{Code: CompareRejectScannerMismatch}
	}
	if oldScan.FingerprintScheme == "" || oldScan.FingerprintScheme != newScan.FingerprintScheme {
		return &CompareReject{Code: CompareRejectFingerprintScheme}
	}
	if oldScan.ExecutionFingerprint == "" || oldScan.ExecutionFingerprint != newScan.ExecutionFingerprint {
		return &CompareReject{Code: CompareRejectDefinitionMismatch}
	}
	if !CoversGeneration(oldScan.CoverageStatus) || !CoversGeneration(newScan.CoverageStatus) {
		return &CompareReject{Code: CompareRejectCoverageIncomplete}
	}
	if oldScan.FindingSetID == "" || newScan.FindingSetID == "" {
		return &CompareReject{Code: CompareRejectCoverageIncomplete}
	}
	return nil
}

func comparisonProjection(ctx context.Context, q *db.Queries, oldScan, newScan *api.CodeScan) (*Comparison, error) {
	raw, err := q.GetScanComparison(ctx, db.GetScanComparisonParams{OldSetID: oldScan.FindingSetID, NewSetID: newScan.FindingSetID})
	if err == nil {
		var response Comparison
		if err := json.Unmarshal([]byte(raw), &response); err != nil {
			return nil, err
		}
		return &response, nil
	}
	if !db.IsNoRows(err) {
		return nil, err
	}
	load := func(id string) ([]api.SecurityFinding, error) {
		row, err := q.GetScanFindingSetForScan(ctx, id)
		if err != nil {
			return nil, err
		}
		return loadFindingEntries(ctx, q, row.ID)
	}
	oldFindings, err := load(oldScan.ID)
	if err != nil {
		return nil, err
	}
	newFindings, err := load(newScan.ID)
	if err != nil {
		return nil, err
	}
	response := buildComparison(oldScan.ID, newScan.ID, oldFindings, newFindings)
	encoded, err := surveyjson.Marshal(response)
	if err != nil {
		return nil, err
	}
	boardJSON, err := surveyjson.Marshal(compactBoardComparison(response))
	if err != nil {
		return nil, err
	}
	err = q.PutScanComparison(ctx, db.PutScanComparisonParams{OldSetID: oldScan.FindingSetID, NewSetID: newScan.FindingSetID, ResponseJson: string(encoded), BoardJson: string(boardJSON)})
	return response, err
}

func compactBoardComparison(response *Comparison) *Comparison {
	compact := *response
	compact.NewFindings = nil
	compact.ResolvedFindings = nil
	compact.PersistedFindings = nil
	for _, stub := range BoardCompareStubsFromFindings(response.NewFindings, boardCompareTopNewCap) {
		compact.NewFindings = append(compact.NewFindings, api.SecurityFinding{
			RuleID: stub.RuleID, Locations: []api.SecurityFindingLocation{{URI: stub.File}},
		})
	}
	return &compact
}

func buildComparison(oldScanID, newScanID string, oldFindings, newFindings []api.SecurityFinding) *Comparison {
	newFull, resolvedFull, persistedFull := diffCompareFindings(oldFindings, newFindings)

	sort.SliceStable(newFull, func(i, j int) bool {
		return api.FindingLevelRank(newFull[i].Level) < api.FindingLevelRank(newFull[j].Level)
	})

	cap := scancfg.DefaultAgentBudget().MaxGuidanceFindings
	listTruncated := false
	newListed, listTruncated := capCompareBucket(newFull, cap, listTruncated)
	resolvedListed, listTruncated := capCompareBucket(resolvedFull, cap, listTruncated)
	persistedListed, listTruncated := capCompareBucket(persistedFull, cap, listTruncated)

	return &Comparison{
		OldScanID:         oldScanID,
		NewScanID:         newScanID,
		NewFindings:       newListed,
		ResolvedFindings:  resolvedListed,
		PersistedFindings: persistedListed,
		NewCount:          len(newFull),
		ResolvedCount:     len(resolvedFull),
		PersistedCount:    len(persistedFull),
		NewByLevel:        compareFindingsByLevel(newFull),
		ResolvedByLevel:   compareFindingsByLevel(resolvedFull),
		Truncated:         listTruncated,
	}
}

// PreviousComplete returns the prior comparable complete scan, within latest's
// delegation when it has one, or nil when none.
func (c *CoordinatorImpl) PreviousComplete(ctx context.Context, latest api.CodeScan) (*api.CodeScan, error) {
	if c == nil || c.Store == nil {
		return nil, fmt.Errorf("scan coordinator not configured")
	}
	return c.Store.PreviousComplete(ctx, latest)
}

type compareMatchIndex struct {
	byPrimary  map[string]int
	byAdvisory map[string][]int
}

func buildCompareIndex(findings []api.SecurityFinding) compareMatchIndex {
	idx := compareMatchIndex{
		byPrimary:  make(map[string]int, len(findings)),
		byAdvisory: make(map[string][]int),
	}
	for i, f := range findings {
		if key := strings.TrimSpace(f.Fingerprints.Primary); key != "" {
			idx.byPrimary[key] = i
		}
		if osv := advisoryOSVID(f); osv != "" {
			idx.byAdvisory[osv] = append(idx.byAdvisory[osv], i)
		}
	}
	return idx
}

func diffCompareFindings(oldFindings, newFindings []api.SecurityFinding) (newBucket, resolvedBucket, persistedBucket []api.SecurityFinding) {
	idx := buildCompareIndex(oldFindings)
	matched := make([]bool, len(oldFindings))

	for _, f := range newFindings {
		if key := strings.TrimSpace(f.Fingerprints.Primary); key != "" {
			if oi, ok := idx.byPrimary[key]; ok && !matched[oi] {
				persistedBucket = append(persistedBucket, f)
				matched[oi] = true
				continue
			}
		}
		if osv := advisoryOSVID(f); osv != "" {
			if matchedAdvisory(idx.byAdvisory[osv], matched) {
				persistedBucket = append(persistedBucket, f)
				continue
			}
		}
		newBucket = append(newBucket, f)
	}

	for i, f := range oldFindings {
		if !matched[i] {
			resolvedBucket = append(resolvedBucket, f)
		}
	}
	return newBucket, resolvedBucket, persistedBucket
}

func matchedAdvisory(indices []int, matched []bool) bool {
	for _, oi := range indices {
		if !matched[oi] {
			matched[oi] = true
			return true
		}
	}
	return false
}

func advisoryOSVID(f api.SecurityFinding) string {
	if f.Properties == nil || f.Properties.Lycaon == nil || f.Properties.Lycaon.Advisory == nil {
		return ""
	}
	return strings.TrimSpace(f.Properties.Lycaon.Advisory.OSVID)
}

func compareFindingsByLevel(findings []api.SecurityFinding) map[string]int {
	out := map[string]int{}
	for _, key := range api.DefaultFindingLevelBucketKeys() {
		out[key] = 0
	}
	for _, f := range findings {
		level := string(f.Level)
		if level == "" {
			level = "unknown"
		}
		if _, ok := out[level]; !ok {
			level = "unknown"
		}
		out[level]++
	}
	return out
}

func capCompareBucket(in []api.SecurityFinding, cap int, truncated bool) ([]api.SecurityFinding, bool) {
	if cap <= 0 || len(in) <= cap {
		return in, truncated
	}
	return in[:cap], true
}

// Bounded comparisons require identical unobserved directories.
func (c *CoordinatorImpl) validateBoundedComparison(ctx context.Context, oldScan, newScan *api.CodeScan) error {
	if oldScan.CoverageStatus != api.ScanCoverageBounded && newScan.CoverageStatus != api.ScanCoverageBounded {
		return nil
	}
	before, err := c.unobservedBoundaries(ctx, oldScan.SourceSnapshotID)
	if err != nil {
		return err
	}
	after, err := c.unobservedBoundaries(ctx, newScan.SourceSnapshotID)
	if err != nil {
		return err
	}
	if !slices.Equal(before, after) {
		return &CompareReject{Code: CompareRejectCoverageIncomplete}
	}
	return nil
}

func (c *CoordinatorImpl) unobservedBoundaries(ctx context.Context, snapshotID string) ([]string, error) {
	rows, err := c.Store.queries.ListSourceSnapshotBoundaries(ctx, snapshotID)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		if (sourcesnapshot.Boundary{Reason: row.Reason}).Budgeted() {
			out = append(out, row.RootPath+"\x00"+row.Path)
		}
	}
	slices.Sort(out)
	return out, nil
}
