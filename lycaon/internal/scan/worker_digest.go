package scan

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/packboard"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
	"github.com/lycaon/lycaon/pkg/api"
)

const (
	workerScanDigestMaxLines = 12
	workerScanDigestMaxPaths = 8
)

// ScanLister returns recent scans for canonical paths.
type ScanLister interface {
	List(ctx context.Context, canonicalPaths []string, limit int) ([]api.CodeScan, error)
}

// WorkerScanLister loads ambient and workflow-bound scan evidence.
type WorkerScanLister interface {
	ScanLister
	WorkflowRunScanLister
}

// WorkerScanDigest builds bounded ambient scan context.
func WorkerScanDigest(ctx context.Context, list ScanLister, projectDir string, now time.Time) []string {
	if list == nil || strings.TrimSpace(projectDir) == "" {
		return nil
	}
	scans, err := list.List(ctx, []string{projectDir}, 1)
	if err != nil || len(scans) == 0 {
		return nil
	}
	s := scans[0]
	summary := &api.BoardScanSummary{
		Status:         s.Status,
		CoverageStatus: s.CoverageStatus,
		WarningSummary: s.WarningSummary,
		Categories:     s.Categories,
		FindingsCount:  s.FindingsCount,
		Error:          strings.TrimSpace(s.Error),
		Trigger:        s.Trigger,
		CreatedAt:      s.CreatedAt,
		CompletedAt:    s.CompletedAt,
		StartedAt:      s.StartedAt,
		LongRunning:    s.LongRunning,
	}
	ApplyBoardRollup(summary, &s)
	if now.IsZero() {
		now = time.Now().UTC()
	}
	lines := []string{
		"## Scan evidence (host)",
		packboard.FormatScansLine(summary, nil, "", now),
	}
	if s.Status != api.CodeScanStatusComplete {
		lines = append(lines, "Start with scan_summary when the scan completes — do not repo-wide grep for findings.")
		return capLines(lines, workerScanDigestMaxLines)
	}
	lines = append(lines,
		fmt.Sprintf("scan_id: %s", strings.TrimSpace(s.ID)),
		"Drill down with scan_summary and scan_query — not manual repo-wide audit.",
	)
	paths := uniqueScanPaths(s.TopLocations, workerScanDigestMaxPaths)
	if len(paths) > 0 {
		lines = append(lines, "Sample flagged paths:")
		for _, p := range paths {
			lines = append(lines, "- "+p)
		}
	}
	return capLines(lines, workerScanDigestMaxLines)
}

// WorkflowWorkerScanDigest builds bounded run-bound scan context.
func WorkflowWorkerScanDigest(ctx context.Context, list WorkflowRunScanLister, workflowRunID string) ([]string, error) {
	workflowRunID = strings.TrimSpace(workflowRunID)
	if list == nil || workflowRunID == "" {
		return nil, fmt.Errorf("workflow scan ledger and run id required")
	}
	scans, err := list.ListByWorkflowRunID(ctx, workflowRunID)
	if err != nil {
		return nil, fmt.Errorf("list scans for workflow run %s: %w", workflowRunID, err)
	}
	if len(scans) == 0 {
		return []string{
			"## Workflow scan evidence (host)",
			"No scans are bound to this workflow run. Treat scan coverage as unavailable.",
		}, nil
	}
	ids := make([]string, 0, len(scans))
	lines := []string{
		"## Workflow scan evidence (host)",
		"These scans are bound to this workflow run. Do not substitute project-ambient scans.",
	}
	allTerminal := true
	rowLimit := workerScanDigestMaxLines - 5
	for i := range scans {
		s := scans[i]
		ids = append(ids, strings.TrimSpace(s.ID))
		if i < rowLimit {
			lines = append(lines, fmt.Sprintf("- `%s` · execution_status=%s; coverage_status=%s; result_available=%t; findings_count=%d", s.ID, s.Status, s.CoverageStatus, s.Status == api.CodeScanStatusComplete || s.FindingsStored > 0 || s.FindingsCount > 0, s.FindingsCount))
		}
		if !StatusTerminal(s.Status) {
			allTerminal = false
		}
	}
	if omitted := len(scans) - rowLimit; omitted > 0 {
		lines = append(lines, fmt.Sprintf("- %d additional bound scan(s); all ids follow", omitted))
	}
	rawIDs, err := surveyjson.Marshal(ids)
	if err != nil {
		return nil, fmt.Errorf("encode workflow scan ids: %w", err)
	}
	lines = append(lines, "scan_ids: "+string(rawIDs))
	if allTerminal {
		lines = append(lines, "Use this exact scan_ids array with scan_summary or scan_query; do not call scan_list to rediscover it.")
	} else {
		lines = append(lines, "The bound set is not terminal; do not chase a different scan from scan_list.")
	}
	return capLines(lines, workerScanDigestMaxLines), nil
}

func uniqueScanPaths(locations []api.BoardScanTopLocation, max int) []string {
	if max <= 0 {
		return nil
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, max)
	for _, location := range locations {
		p := strings.TrimSpace(location.URI)
		if p == "" {
			continue
		}
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
		if len(out) >= max {
			break
		}
	}
	return out
}

func capLines(lines []string, max int) []string {
	if max <= 0 || len(lines) <= max {
		return lines
	}
	return lines[:max]
}
