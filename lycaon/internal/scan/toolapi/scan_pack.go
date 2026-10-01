package toolapi

import (
	"context"
	"crypto/sha256"
	"fmt"
	"sort"
	"strings"
	"time"

	scanbase "github.com/lycaon/lycaon/internal/scan"
	scanfindings "github.com/lycaon/lycaon/internal/scan/findings"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
	"github.com/lycaon/lycaon/pkg/api"
)

// Unstarted full-pass members have no scan; PassPhase explains their state.
type ScanPackPerScan struct {
	ScanID                string                   `json:"scan_id,omitempty"`
	ScannerID             string                   `json:"scanner_id,omitempty"`
	PassPhase             api.FullPassMemberPhase  `json:"pass_phase,omitempty"`
	Categories            []api.ScanCategory       `json:"categories,omitempty"`
	Status                api.CodeScanStatus       `json:"status"`
	FindingsCount         int                      `json:"findings_count,omitempty"`
	FindingsByLevel       map[string]int           `json:"findings_by_level,omitempty"`
	ScanScope             string                   `json:"scan_scope,omitempty"`
	SupportedQueryFilters []string                 `json:"supported_query_filters,omitempty"`
	Warnings              []api.ScanWarning        `json:"warnings,omitempty"`
	WarningSummary        []api.ScanWarningSummary `json:"warning_summary,omitempty"`
	CoverageStatus        api.ScanCoverageStatus   `json:"coverage_status,omitempty"`
	// Progress is how far a chunked pass has come; Delta is what a
	// path-scoped scan introduced and fixed against its base.
	Progress *api.ScanProgress         `json:"progress,omitempty"`
	Delta    *api.ScanDelta            `json:"delta,omitempty"`
	Error    string                    `json:"error,omitempty"`
	Guidance []api.ScanGuidanceSummary `json:"guidance_preview,omitempty"`
}

type ScanPackToolResult struct {
	PassID          string                    `json:"pass_id,omitempty"`
	ScanIDs         []string                  `json:"scan_ids"`
	PerScan         []ScanPackPerScan         `json:"per_scan"`
	Status          api.CodeScanStatus        `json:"status"`
	FindingsCount   int                       `json:"findings_count,omitempty"`
	Error           string                    `json:"error,omitempty"`
	GuidancePreview []api.ScanGuidanceSummary `json:"guidance_preview,omitempty"`
	FindingsStored  int                       `json:"findings_stored,omitempty"`
	Findings        []api.SecurityFinding     `json:"findings,omitempty"`
	Message         string                    `json:"message,omitempty"`
	WaitTimedOut    bool                      `json:"wait_timed_out,omitempty"`
}

type ReceiptOptions struct {
	full         bool
	waitTimedOut bool
}

func scanPackReceipt(ctx context.Context, coord scanbase.ScanCoordinator, scanIDs []string, options ReceiptOptions) (string, error) {
	out, err := scanPackResult(ctx, coord, scanIDs, options)
	if err != nil {
		return "", err
	}
	return encodeScanPackResult(out)
}

// Include unstarted members; aggregate results only after every member finishes.
func FullPassReceipt(ctx context.Context, coord scanbase.ScanCoordinator, pass scanbase.FullPass, options ReceiptOptions) (string, error) {
	out := ScanPackToolResult{ScanIDs: pass.ScanIDs(), PerScan: []ScanPackPerScan{}}
	if len(out.ScanIDs) > 0 {
		started, err := scanPackResult(ctx, coord, out.ScanIDs, options)
		if err != nil {
			return "", err
		}
		out = started
	}
	out.PassID = pass.ID
	waiting := false
	var missing []string
	for _, member := range pass.Members {
		switch member.Phase {
		case api.FullPassMemberWaitingForScanner, api.FullPassMemberWaitingForPass:
			waiting = true
			out.PerScan = append(out.PerScan, ScanPackPerScan{
				ScannerID: member.ScannerID, Status: api.CodeScanStatusPending, PassPhase: member.Phase,
			})
		case api.FullPassMemberNotStarted:
			missing = append(missing, member.ScannerID)
			out.PerScan = append(out.PerScan, ScanPackPerScan{
				ScannerID: member.ScannerID, Status: api.CodeScanStatusCanceled, PassPhase: member.Phase,
			})
		case api.FullPassMemberStarted:
		}
	}
	switch {
	case waiting:
		out.Status = api.CodeScanStatusPending
		out.FindingsStored, out.Findings, out.GuidancePreview = 0, nil, nil
		if options.waitTimedOut {
			out.WaitTimedOut = true
			out.Message = "scan wait timed out; the full pass continues in the background. Wait with a scan_done condition, then call scan_summary with this pass_id. Do not re-run scan_pack for the same scope."
		} else {
			out.Message = "full pass requested; its scanners start together once each has finished earlier work. Wait with a scan_done condition, then call scan_summary with this pass_id for its scan_ids and cross-engine aggregate. Do not re-run scan_pack for the same scope."
		}
	case len(missing) > 0:
		out.Status = api.CodeScanStatusFailed
		out.FindingsStored, out.Findings, out.GuidancePreview = 0, nil, nil
		out.Error = fmt.Sprintf("scanner(s) %s did not start", strings.Join(missing, ", "))
		out.Message = "one or more scanners did not start, so this pass covers less than was requested; read per_scan"
	}
	return encodeScanPackResult(out)
}

// waitForFullPass blocks until the pass finishes or ctx ends and returns its latest state.
func waitForFullPass(ctx context.Context, coord scanbase.ScanCoordinator, passID string) (scanbase.FullPass, error) {
	store := scanbase.StoreFromCoordinator(coord)
	if store == nil {
		return scanbase.FullPass{}, fmt.Errorf("scan coordinator not configured")
	}
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		pass, err := store.FullPass(ctx, passID)
		if err != nil {
			return scanbase.FullPass{}, err
		}
		if pass == nil {
			return scanbase.FullPass{}, fmt.Errorf("full pass %s not found", passID)
		}
		if pass.Finished() {
			return *pass, nil
		}
		select {
		case <-ctx.Done():
			return *pass, ctx.Err()
		case <-ticker.C:
		}
	}
}

func scanPackResult(ctx context.Context, coord scanbase.ScanCoordinator, scanIDs []string, options ReceiptOptions) (ScanPackToolResult, error) {
	store := scanbase.StoreFromCoordinator(coord)
	if store == nil {
		return ScanPackToolResult{}, fmt.Errorf("scan coordinator not configured")
	}
	scanIDs = normalizeScanIDs(scanIDs)
	if len(scanIDs) == 0 {
		return ScanPackToolResult{}, fmt.Errorf("no scan ids")
	}
	var (
		totalFindings int
		anyFailed     bool
		anyRunning    bool
		errMsg        string
		perScan       []ScanPackPerScan
		records       []*api.CodeScan
	)
	for _, id := range scanIDs {
		rec, err := store.Get(ctx, id)
		if err != nil {
			return ScanPackToolResult{}, err
		}
		if rec == nil {
			anyRunning = true
			perScan = append(perScan, ScanPackPerScan{ScanID: id, Status: api.CodeScanStatusPending})
			continue
		}
		records = append(records, rec)
		row := ScanPackPerScan{
			ScanID:                rec.ID,
			ScannerID:             rec.ScannerID,
			Categories:            rec.Categories,
			Status:                rec.Status,
			FindingsCount:         rec.FindingsCount,
			FindingsByLevel:       rec.FindingsByLevel,
			ScanScope:             rec.ScanScope,
			SupportedQueryFilters: rec.SupportedQueryFilters,
			CoverageStatus:        rec.CoverageStatus,
			Error:                 rec.Error,
		}
		warnings := rec.Warnings
		if warnings == nil {
			warnings = scanbase.WarningsFromResult(rec.Result)
		}
		row.WarningSummary = scanbase.SummarizeWarnings(warnings)
		if warnings == nil {
			row.WarningSummary = rec.WarningSummary
		}
		if options.full {
			row.Warnings = warnings
		}
		totalFindings += rec.FindingsCount
		switch rec.Status {
		case api.CodeScanStatusFailed, api.CodeScanStatusTimedOut,
			api.CodeScanStatusCanceled, api.CodeScanStatusSuperseded:
			anyFailed = true
			if rec.Error != "" {
				errMsg = rec.Error
			}
		case api.CodeScanStatusComplete:
		default:
			anyRunning = true
		}
		perScan = append(perScan, row)
	}
	out := ScanPackToolResult{
		ScanIDs:       scanIDs,
		PerScan:       perScan,
		FindingsCount: totalFindings,
		Error:         errMsg,
	}
	if !anyRunning && !anyFailed && len(records) == len(scanIDs) {
		merged, guidance, err := aggregateScanPack(ctx, store, records)
		if err != nil {
			return ScanPackToolResult{}, err
		}
		out.FindingsStored = len(merged)
		out.GuidancePreview = previewPackGuidance(guidance, 12)
		if options.full {
			out.Findings = merged
			out.GuidancePreview = guidance
		}
	}
	switch {
	case anyRunning:
		out.Status = api.CodeScanStatusPending
		if options.waitTimedOut {
			out.WaitTimedOut = true
			out.Message = "scan wait timed out; scans continue in the background. Wait with a scan_done condition, then pass this exact scan_ids array to scan_summary or scan_query. Do not re-run scan_pack for the same scope."
		} else {
			out.Message = "scan enqueued; wait with a scan_done condition parks until it finishes, then pass this exact scan_ids array to scan_summary or scan_query for a cross-engine aggregate. Do not re-run scan_pack for the same scope."
		}
	case anyFailed:
		out.Status = api.CodeScanStatusFailed
		out.Message = "one or more scanners failed; read per_scan error fields or GET /v1/projects/{id}/scans/{scan_id}?view=full"
	case packHasIncompleteCoverage(perScan):
		out.Status = api.CodeScanStatusComplete
		out.Message = "scan finished with incomplete coverage; review per_scan coverage_status and warning_summary, and use scan_summary with view=full for diagnostic locations"
	case totalFindings == 0:
		out.Status = api.CodeScanStatusComplete
		out.Message = "scan complete with zero findings"
	default:
		out.Status = api.CodeScanStatusComplete
		out.Message = fmt.Sprintf("scan complete with %d raw finding(s) and %d package-aware merged finding(s) across %d engine(s); pass scan_ids to scan_query", totalFindings, out.FindingsStored, len(scanIDs))
	}
	return out, nil
}

func packHasIncompleteCoverage(rows []ScanPackPerScan) bool {
	for _, row := range rows {
		if len(row.WarningSummary) > 0 || (row.CoverageStatus != "" && row.CoverageStatus != api.ScanCoverageComplete) {
			return true
		}
	}
	return false
}

type scanPackWaitResult struct {
	index  int
	record *api.CodeScan
	err    error
}

func waitForScanPack(ctx context.Context, coord scanbase.ScanCoordinator, scanIDs []string) ([]string, error) {
	store := scanbase.StoreFromCoordinator(coord)
	if store == nil {
		return nil, fmt.Errorf("scan coordinator not configured")
	}
	ids := normalizeScanIDs(scanIDs)
	if len(ids) == 0 {
		return nil, fmt.Errorf("no scan ids")
	}
	waitCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	results := make(chan scanPackWaitResult, len(ids))
	for index, id := range ids {
		go func() {
			record, err := scanbase.WaitForScanID(waitCtx, store, id, 250*time.Millisecond)
			results <- scanPackWaitResult{index: index, record: record, err: err}
		}()
	}
	var firstErr error
	for range ids {
		result := <-results
		if result.record != nil && strings.TrimSpace(result.record.ID) != "" {
			ids[result.index] = strings.TrimSpace(result.record.ID)
		}
		if result.err != nil && firstErr == nil {
			firstErr = result.err
			cancel()
		}
	}
	return normalizeScanIDs(ids), firstErr
}

func aggregateScanPack(ctx context.Context, store *scanbase.SQLStore, records []*api.CodeScan) ([]api.SecurityFinding, []api.ScanGuidanceSummary, error) {
	if store == nil || len(records) == 0 {
		return nil, nil, nil
	}
	canonicalPath := records[0].CanonicalPath
	snapshotID := records[0].SourceSnapshotID
	basePaths, err := store.LoadPaths(ctx, records[0].ID)
	if err != nil {
		return nil, nil, err
	}
	baseScope := scanPathsIdentity(basePaths)
	var findings []api.SecurityFinding
	var guidance []api.ScanGuidanceSummary
	for _, record := range records {
		if record == nil || record.Status != api.CodeScanStatusComplete {
			return nil, nil, fmt.Errorf("scan pack contains an incomplete scan")
		}
		paths, err := store.LoadPaths(ctx, record.ID)
		if err != nil {
			return nil, nil, err
		}
		if record.CanonicalPath != canonicalPath || record.SourceSnapshotID != snapshotID || scanPathsIdentity(paths) != baseScope {
			return nil, nil, fmt.Errorf("scan pack members do not share one source snapshot and path scope")
		}
		findings = append(findings, record.Findings...)
		guidance = append(guidance, record.Guidance...)
	}
	return scanfindings.MergeByAdvisory(findings), dedupePackGuidance(guidance), nil
}

func scanPathsIdentity(paths []string) string {
	normalized := scanbase.NormalizeScanPaths(paths)
	raw, _ := surveyjson.Marshal(normalized)
	return string(raw)
}

func dedupePackGuidance(rows []api.ScanGuidanceSummary) []api.ScanGuidanceSummary {
	sort.SliceStable(rows, func(i, j int) bool {
		return api.FindingLevelRank(scanfindings.NormalizeVendorSeverity(rows[i].Severity)) < api.FindingLevelRank(scanfindings.NormalizeVendorSeverity(rows[j].Severity))
	})
	seen := make(map[string]struct{}, len(rows))
	out := make([]api.ScanGuidanceSummary, 0, len(rows))
	for _, row := range rows {
		key := strings.Join([]string{row.Code, row.RuleID, row.File, fmt.Sprint(row.Line)}, "\x00")
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, row)
	}
	return out
}

func previewPackGuidance(rows []api.ScanGuidanceSummary, limit int) []api.ScanGuidanceSummary {
	if len(rows) <= limit {
		return rows
	}
	return rows[:limit]
}

func loadScanPackRecords(ctx context.Context, store *scanbase.SQLStore, scanIDs []string) ([]*api.CodeScan, error) {
	if store == nil {
		return nil, fmt.Errorf("scan store not configured")
	}
	scanIDs = normalizeScanIDs(scanIDs)
	if len(scanIDs) == 0 {
		return nil, fmt.Errorf("scan_ids required")
	}
	records := make([]*api.CodeScan, 0, len(scanIDs))
	for _, id := range scanIDs {
		record, err := store.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		if record == nil {
			return nil, &scanbase.DrilldownReject{Code: scanbase.DrilldownRejectNotFound, Data: map[string]any{"scan_id": id}}
		}
		if record.Status != api.CodeScanStatusComplete {
			return nil, &scanbase.DrilldownReject{
				Code: scanbase.DrilldownRejectNotComplete,
				Data: map[string]any{"scan_id": id, "status": string(record.Status)},
			}
		}
		records = append(records, record)
	}
	return records, nil
}

func normalizeScanIDs(scanIDs []string) []string {
	seen := make(map[string]struct{}, len(scanIDs))
	out := make([]string, 0, len(scanIDs))
	for _, raw := range scanIDs {
		id := strings.TrimSpace(raw)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

func scanPackIdentity(scanIDs []string) string {
	hash := sha256.Sum256([]byte(strings.Join(normalizeScanIDs(scanIDs), "\x00")))
	return fmt.Sprintf("pack:%x", hash[:8])
}

func encodeScanPackResult(out ScanPackToolResult) (string, error) {
	raw, err := surveyjson.Marshal(out)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}
