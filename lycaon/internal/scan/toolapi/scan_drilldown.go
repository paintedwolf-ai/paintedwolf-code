package toolapi

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/guidance"
	scanbase "github.com/lycaon/lycaon/internal/scan"
	scancfg "github.com/lycaon/lycaon/internal/scan/configuration"
	scanfindings "github.com/lycaon/lycaon/internal/scan/findings"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
	"github.com/lycaon/lycaon/pkg/api"
)

const (
	toolScanList    = "scan_list"
	toolScanSummary = "scan_summary"
	toolScanQuery   = "scan_query"

	// ScanProjectDirHostBoundCode rejects caller-supplied project_dir on host-bound scan tools.
	ScanProjectDirHostBoundCode = "SCAN_PROJECT_DIR_HOST"
)

func rejectSuppliedProjectDir(args map[string]any, sessionProjectDir string) error {
	sessionProjectDir = strings.TrimSpace(sessionProjectDir)
	if sessionProjectDir == "" {
		return fmt.Errorf("session has no project_dir")
	}
	sessionProjectDir = filepath.Clean(sessionProjectDir)
	supplied := strings.TrimSpace(drilldownStringArg(args, "project_dir"))
	if supplied == "" {
		return nil
	}
	if filepath.Clean(supplied) == sessionProjectDir {
		return nil
	}
	return &tools.ToolReject{
		Code: ScanProjectDirHostBoundCode,
		Data: map[string]any{"supplied": supplied},
	}
}

func runScanList(ctx context.Context, args map[string]any, tctx tools.ToolContext, coord scanbase.ScanCoordinator) (string, error) {
	projectDir := strings.TrimSpace(tctx.ActiveRootPath())
	if err := rejectSuppliedProjectDir(args, projectDir); err != nil {
		return "", err
	}
	limit := drilldownIntArg(args, "limit", 20)
	scans, err := coord.List(ctx, []string{projectDir}, limit)
	if err != nil {
		return "", err
	}
	return marshalDrilldownJSON(NewListScansResponse(ctx, scans))
}

func runScanSummary(ctx context.Context, args map[string]any, tctx tools.ToolContext, coord scanbase.ScanCoordinator, rejectFmt *guidance.StaticRejectFormatter) (string, error) {
	if err := rejectSuppliedProjectDir(args, tctx.ActiveRootPath()); err != nil {
		return "", err
	}
	scanIDs := drilldownStringSliceArg(args, "scan_ids")
	tctx.SetDisplaySubject(scanDisplaySubject(ctx, tctx, coord, scanIDs))
	passID := strings.TrimSpace(drilldownStringArg(args, "pass_id"))
	view, err := scanbase.ParseScanView(drilldownStringArg(args, "view"))
	if err != nil {
		return "", err
	}
	switch {
	case passID != "" && len(scanIDs) > 0:
		return "", fmt.Errorf("give scan_ids or pass_id, not both")
	case passID != "":
		captureFullPassSubject(ctx, tctx, coord, passID)
		return SummarizeFullPass(ctx, coord, passID, view, rejectFmt)
	case len(scanIDs) == 0:
		return "", fmt.Errorf("scan_ids or pass_id is required")
	}
	if len(scanIDs) > 1 {
		return scanPackReceipt(ctx, coord, scanIDs, ReceiptOptions{full: view == "full"})
	}
	scanID := scanIDs[0]
	read := coord.Summary
	if view == "full" {
		read = coord.Get
	}
	rec, err := read(ctx, scanID)
	if err != nil {
		return "", err
	}
	if rec == nil {
		return "", scanbase.FormatDrilldownReject(&scanbase.DrilldownReject{
			Code: scanbase.DrilldownRejectNotFound,
			Data: map[string]any{"scan_id": scanID},
		}, rejectFmt)
	}
	if trimmed := scanbase.ApplyScanView(rec, view); trimmed != nil {
		rec = trimmed
	}
	return marshalDrilldownJSON(rec)
}

// SummarizeFullPass reads a full pass by id, including members that have not started.
func SummarizeFullPass(ctx context.Context, coord scanbase.ScanCoordinator, passID, view string, rejectFmt *guidance.StaticRejectFormatter) (string, error) {
	store := scanbase.StoreFromCoordinator(coord)
	if store == nil {
		return "", fmt.Errorf("scan coordinator not configured")
	}
	pass, err := store.FullPass(ctx, passID)
	if err != nil {
		return "", err
	}
	if pass == nil {
		return "", scanbase.FormatDrilldownReject(&scanbase.DrilldownReject{
			Code: scanbase.DrilldownRejectPassNotFound,
			Data: map[string]any{"pass_id": passID},
		}, rejectFmt)
	}
	return FullPassReceipt(ctx, coord, *pass, ReceiptOptions{full: view == "full"})
}

func runScanQuery(ctx context.Context, args map[string]any, tctx tools.ToolContext, coord scanbase.ScanCoordinator, rejectFmt *guidance.StaticRejectFormatter) (string, error) {
	if err := rejectSuppliedProjectDir(args, tctx.ActiveRootPath()); err != nil {
		return "", err
	}
	scanIDs := drilldownStringSliceArg(args, "scan_ids")
	tctx.SetDisplaySubject(scanDisplaySubject(ctx, tctx, coord, scanIDs))
	if len(scanIDs) == 0 {
		return "", fmt.Errorf("scan_ids is required")
	}
	if drilldownStringArg(args, "view") == "coverage" {
		out, err := queryCoverage(ctx, coord, scanIDs, args)
		return out, scanbase.MapDrilldownReject(err, rejectFmt)
	}
	for _, field := range []string{"warning_kind", "construct"} {
		if _, present := args[field]; present {
			return "", fmt.Errorf("field %q requires view coverage", field)
		}
	}
	scanID := scanIDs[0]
	req := scanbase.QueryRequest{
		ScanID:      scanID,
		RuleID:      strings.TrimSpace(drilldownStringArg(args, "rule_id")),
		Path:        strings.TrimSpace(drilldownStringArg(args, "path")),
		Code:        strings.TrimSpace(drilldownStringArg(args, "code")),
		Level:       strings.TrimSpace(drilldownStringArg(args, "level")),
		Kind:        strings.TrimSpace(drilldownStringArg(args, "kind")),
		AdvisoryID:  strings.TrimSpace(drilldownStringArg(args, "advisory_id")),
		Fingerprint: strings.TrimSpace(drilldownStringArg(args, "fingerprint")),
		Cursor:      drilldownStringArg(args, "cursor"),
		Offset:      drilldownIntArg(args, "offset", 0),
		Limit:       drilldownIntArg(args, "limit", 0),
		Dedupe:      drilldownBoolArg(args, "dedupe"),
	}
	for name, target := range map[string]**time.Time{"introduced_since": &req.IntroducedSince, "fixed_since": &req.FixedSince} {
		raw := strings.TrimSpace(drilldownStringArg(args, name))
		if raw == "" {
			continue
		}
		at, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return "", fmt.Errorf("%s must be an RFC 3339 time", name)
		}
		*target = &at
	}
	if view := drilldownStringArg(args, "view"); view == "groups" {
		out, err := queryFindingGroups(ctx, coord, scanIDs, req)
		return out, scanbase.MapDrilldownReject(err, rejectFmt)
	} else if view != "" && view != "findings" {
		return "", fmt.Errorf("view must be findings, groups, or coverage")
	}
	var resp *api.ScanQueryResponse
	var err error
	if len(scanIDs) > 1 {
		resp, err = queryScanPack(ctx, scanbase.StoreFromCoordinator(coord), scanIDs, req)
	} else {
		resp, err = coord.Query(ctx, req)
	}
	if err != nil {
		return "", scanbase.MapDrilldownReject(err, rejectFmt)
	}
	return marshalDrilldownJSON(resp)
}

func queryFindingGroups(ctx context.Context, coord scanbase.ScanCoordinator, ids []string, req scanbase.QueryRequest) (string, error) {
	var findings []api.SecurityFinding
	for _, id := range ids {
		rec, err := coord.Get(ctx, id)
		if err != nil {
			return "", err
		}
		if rec == nil {
			return "", &scanbase.DrilldownReject{Code: scanbase.DrilldownRejectNotFound, Data: map[string]any{"scan_id": id}}
		}
		if rec.Status != api.CodeScanStatusComplete {
			return "", &scanbase.DrilldownReject{Code: scanbase.DrilldownRejectNotComplete, Data: map[string]any{"scan_id": id, "status": string(rec.Status)}}
		}
		findings = append(findings, scanbase.FilterFindings(rec.Findings, req)...)
	}
	groups := scanfindings.GroupFindings(findings, 10)
	offset := min(max(req.Offset, 0), len(groups))
	limit := req.Limit
	if limit <= 0 {
		limit = scancfg.DefaultAgentBudget().MaxQueryResults
	}
	limit = min(limit, 100)
	end := min(offset+limit, len(groups))
	var next *int
	if end < len(groups) {
		next = &end
	}
	return marshalDrilldownJSON(struct {
		ScanIDs    []string                    `json:"scan_ids"`
		Groups     []scanfindings.FindingGroup `json:"groups"`
		TotalMatch int                         `json:"total_match"`
		Offset     int                         `json:"offset"`
		NextOffset *int                        `json:"next_offset,omitempty"`
		Truncated  bool                        `json:"truncated"`
	}{ids, groups[offset:end], len(groups), offset, next, next != nil})
}

func queryScanPack(ctx context.Context, store *scanbase.SQLStore, scanIDs []string, req scanbase.QueryRequest) (*api.ScanQueryResponse, error) {
	records, err := loadScanPackRecords(ctx, store, scanIDs)
	if err != nil {
		return nil, err
	}
	findings, guidance, err := aggregateScanPack(ctx, store, records)
	if err != nil {
		return nil, err
	}
	req.ScanID = scanPackIdentity(scanIDs)
	return scanbase.QuerySecurityFindings(req.ScanID, findings, guidance, req)
}

func marshalDrilldownJSON(v any) (string, error) {
	raw, err := surveyjson.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func drilldownStringArg(args map[string]any, key string) string {
	if args == nil {
		return ""
	}
	v, ok := args[key]
	if !ok {
		return ""
	}
	s, _ := v.(string)
	return s
}

func drilldownStringSliceArg(args map[string]any, key string) []string {
	if args == nil {
		return nil
	}
	switch values := args[key].(type) {
	case []string:
		return normalizeScanIDs(values)
	case []any:
		out := make([]string, 0, len(values))
		for _, value := range values {
			if text, ok := value.(string); ok {
				out = append(out, text)
			}
		}
		return normalizeScanIDs(out)
	default:
		return nil
	}
}

func drilldownIntArg(args map[string]any, key string, def int) int {
	if args == nil {
		return def
	}
	v, ok := args[key]
	if !ok {
		return def
	}
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	default:
		return def
	}
}

func drilldownBoolArg(args map[string]any, key string) bool {
	if args == nil {
		return false
	}
	v, ok := args[key]
	if !ok {
		return false
	}
	b, _ := v.(bool)
	return b
}
