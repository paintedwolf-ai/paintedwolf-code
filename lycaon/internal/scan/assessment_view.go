package scan

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/db"
	scanfindings "github.com/lycaon/lycaon/internal/scan/findings"
	"github.com/lycaon/lycaon/pkg/api"
)

// AssessmentView distinguishes the newest attempt from the newest complete,
// fully covered assessment that remains usable as security authority.
type AssessmentView struct {
	CurrentID       string
	Current         []api.CodeScan
	PreviousID      string
	Previous        []api.CodeScan
	LatestAttemptID string
	LatestAttempt   []api.CodeScan
	CurrentSummary  *api.BoardScanSummary
	LatestSummary   *api.BoardScanSummary
}

type assessmentRow struct {
	id       string
	required []string
}

// AssessmentView returns assessment aggregates newest first across paths.
func (s *SQLStore) AssessmentView(ctx context.Context, canonicalPaths []string) (AssessmentView, error) {
	paths := uniqueNonEmpty(canonicalPaths)
	if len(paths) == 0 {
		return AssessmentView{}, nil
	}
	var rows []db.BoardAssessmentCandidatesRow
	for _, path := range paths {
		part, err := s.queries.BoardAssessmentCandidates(ctx, path)
		if err != nil {
			return AssessmentView{}, err
		}
		rows = append(rows, part...)
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].CreatedAt == rows[j].CreatedAt {
			return rows[i].AdmissionOrder > rows[j].AdmissionOrder
		}
		return rows[i].CreatedAt > rows[j].CreatedAt
	})
	assessments := make([]assessmentRow, 0, len(rows))
	assessmentIDs := make([]string, 0, len(rows))
	for _, stored := range rows {
		row := assessmentRow{id: stored.ID}
		if err := json.Unmarshal([]byte(stored.RequiredScannersJson), &row.required); err != nil {
			return AssessmentView{}, err
		}
		assessments = append(assessments, row)
		assessmentIDs = append(assessmentIDs, row.id)
	}
	if len(assessmentIDs) == 0 {
		return AssessmentView{}, nil
	}
	bindings, err := s.queries.ListAssessmentScanBindings(ctx, assessmentIDs)
	if err != nil {
		return AssessmentView{}, err
	}
	scanIDs := make([]string, 0, len(bindings))
	seenScanIDs := make(map[string]struct{}, len(bindings))
	for _, binding := range bindings {
		if _, seen := seenScanIDs[binding.ScanID]; seen {
			continue
		}
		seenScanIDs[binding.ScanID] = struct{}{}
		scanIDs = append(scanIDs, binding.ScanID)
	}
	allScans, err := s.metadata(ctx, scanIDs)
	if err != nil {
		return AssessmentView{}, err
	}
	scansByID := make(map[string]api.CodeScan, len(allScans))
	for index := range allScans {
		scansByID[allScans[index].ID] = allScans[index]
	}
	byAssessment := make(map[string][]api.CodeScan, len(assessments))
	for _, binding := range bindings {
		scan, ok := scansByID[binding.ScanID]
		if !ok {
			continue
		}
		scan.AssessmentID = binding.AssessmentID
		byAssessment[binding.AssessmentID] = append(byAssessment[binding.AssessmentID], scan)
	}
	view := AssessmentView{}
	for _, assessment := range assessments {
		members := byAssessment[assessment.id]
		if view.LatestAttemptID == "" {
			view.LatestAttemptID = assessment.id
			view.LatestAttempt = append([]api.CodeScan(nil), members...)
		}
		if view.CurrentID == "" && assessmentComplete(assessment.required, members) {
			view.CurrentID = assessment.id
			view.Current = append([]api.CodeScan(nil), members...)
		} else if view.CurrentID != "" && view.PreviousID == "" && assessmentComplete(assessment.required, members) {
			view.PreviousID = assessment.id
			view.Previous = append([]api.CodeScan(nil), members...)
		}
		if view.CurrentID != "" && view.PreviousID != "" && view.LatestAttemptID != "" {
			break
		}
	}
	if view.CurrentSummary, err = s.assessmentSummary(ctx, view.CurrentID, view.Current); err != nil {
		return AssessmentView{}, err
	}
	if view.LatestSummary, err = s.assessmentSummary(ctx, view.LatestAttemptID, view.LatestAttempt); err != nil {
		return AssessmentView{}, err
	}
	return view, nil
}

func assessmentComplete(required []string, scans []api.CodeScan) bool {
	if len(required) == 0 || len(scans) == 0 {
		return false
	}
	complete := make(map[string]bool, len(scans))
	for _, scan := range scans {
		if EstablishesAuthority(scan) {
			complete[scan.ScannerID] = true
		}
	}
	for _, scannerID := range required {
		if !complete[scannerID] {
			return false
		}
	}
	return true
}

// BuildAssessmentSummary aggregates member facts without reinterpreting their
// typed run or coverage outcomes.
func BuildAssessmentSummary(assessmentID string, scans []api.CodeScan) *api.BoardScanSummary {
	if strings.TrimSpace(assessmentID) == "" || len(scans) == 0 {
		return nil
	}
	out := &api.BoardScanSummary{
		AssessmentID: assessmentID, ScannerCount: len(scans),
		Status: api.CodeScanStatusComplete, CoverageStatus: api.ScanCoverageComplete,
	}
	categorySet := map[api.ScanCategory]struct{}{}
	var findings []api.SecurityFinding
	var warnings []api.ScanWarning
	statuses := make(map[api.CodeScanStatus]bool)
	coverageMissing := false
	for index := range scans {
		scan := &scans[index]
		if index == 0 || scan.CreatedAt.After(out.CreatedAt) {
			out.CreatedAt = scan.CreatedAt
			out.Trigger = scan.Trigger
			out.SourceSnapshotID = scan.SourceSnapshotID
			out.HeadShort = ShortHeadSHA(scan.HeadSHA)
		}
		if scan.CompletedAt != nil && (out.CompletedAt == nil || scan.CompletedAt.After(*out.CompletedAt)) {
			completed := *scan.CompletedAt
			out.CompletedAt = &completed
		}
		if scan.StartedAt != nil && (out.StartedAt == nil || scan.StartedAt.Before(*out.StartedAt)) {
			started := *scan.StartedAt
			out.StartedAt = &started
		}
		out.LongRunning = out.LongRunning || scan.LongRunning
		if scan.FindingSetID != "" {
			out.FindingsCount += len(scan.Findings)
		} else {
			out.FindingsCount += scan.FindingsCount
		}
		warnings = append(warnings, scan.Warnings...)
		findings = append(findings, scan.Findings...)
		for _, category := range scan.Categories {
			categorySet[category] = struct{}{}
		}
		statuses[scan.Status] = true
		if out.Error == "" && scan.Error != "" {
			out.Error = scan.Error
		}
		if scan.CoverageStatus == "" {
			coverageMissing = true
		} else if scan.CoverageStatus == api.ScanCoverageUnavailable {
			out.CoverageStatus = api.ScanCoverageUnavailable
		} else if coverageRank(scan.CoverageStatus) < coverageRank(out.CoverageStatus) {
			out.CoverageStatus = scan.CoverageStatus
		}
	}
	out.Status = aggregateAssessmentStatus(statuses)
	if coverageMissing {
		out.CoverageStatus = ""
	}
	for category := range categorySet {
		out.Categories = append(out.Categories, category)
	}
	sort.Slice(out.Categories, func(i, j int) bool { return out.Categories[i] < out.Categories[j] })
	out.FindingsByLevel = scanfindings.CountFindingsByLevel(findings)
	out.FindingsByKind = CountFindingsByKind(findings)
	out.UnmappedCount = CountUnmappedSAST(findings)
	out.WarningSummary = SummarizeWarnings(warnings)
	out.TopLocations = TopLocationsFromFindings(findings, boardTopLocationsStoreCap)
	return out
}

func aggregateAssessmentStatus(statuses map[api.CodeScanStatus]bool) api.CodeScanStatus {
	for _, status := range []api.CodeScanStatus{
		api.CodeScanStatusRunning,
		api.CodeScanStatusPending,
		api.CodeScanStatusFailed,
		api.CodeScanStatusTimedOut,
		api.CodeScanStatusCanceled,
		api.CodeScanStatusSuperseded,
		api.CodeScanStatusComplete,
	} {
		if statuses[status] {
			return status
		}
	}
	return api.CodeScanStatusPending
}

// LatestAssessmentForPaths exposes the aggregate view to board projections.
func (c *CoordinatorImpl) LatestAssessmentForPaths(ctx context.Context, canonicalPaths []string) (AssessmentView, error) {
	if c == nil || c.Store == nil {
		return AssessmentView{}, fmt.Errorf("scan coordinator not configured")
	}
	return c.Store.AssessmentView(ctx, canonicalPaths)
}

// coverageRank orders coverage from what proves least to what proves most;
// an assessment's coverage is its weakest member's.
func coverageRank(status api.ScanCoverageStatus) int {
	switch status {
	case api.ScanCoverageUnavailable:
		return 0
	case api.ScanCoveragePartial:
		return 1
	case api.ScanCoverageBounded:
		return 2
	case api.ScanCoverageComplete:
		return 3
	}
	return 3
}
