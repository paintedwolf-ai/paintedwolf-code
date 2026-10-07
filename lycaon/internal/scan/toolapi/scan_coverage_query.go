package toolapi

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/lycaon/lycaon/internal/runeclamp"
	scanbase "github.com/lycaon/lycaon/internal/scan"
	scancoverage "github.com/lycaon/lycaon/internal/scan/coverage"
	"github.com/lycaon/lycaon/pkg/api"
)

type coverageLocation struct {
	Message          string              `json:"message,omitempty"`
	MessageTruncated bool                `json:"message_truncated,omitempty"`
	ScanID           string              `json:"scan_id"`
	Kind             api.ScanWarningKind `json:"kind"`
	Path             string              `json:"path,omitempty"`
	Line             int                 `json:"line,omitempty"`
	Column           int                 `json:"column,omitempty"`
	Construct        string              `json:"construct,omitempty"`
	RuleID           string              `json:"rule_id,omitempty"`
}

type coverageWarning struct {
	api.ScanWarning
	ScanID string
}

func queryCoverage(ctx context.Context, coord scanbase.ScanCoordinator, ids []string, args map[string]any) (string, error) {
	for key := range args {
		switch key {
		case "scan_ids", "view", "warning_kind", "construct", "path", "rule_id", "offset", "limit":
		default:
			return "", fmt.Errorf("field %q is not supported by coverage queries", key)
		}
	}
	var warnings []coverageWarning
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
		source := rec.Warnings
		if source == nil {
			source = scanbase.WarningsFromResult(rec.Result)
		}
		for _, warning := range source {
			warnings = append(warnings, coverageWarning{ScanWarning: warning, ScanID: id})
		}
	}
	prefix := strings.TrimSuffix(strings.TrimSpace(drilldownStringArg(args, "path")), "/")
	prefix = strings.TrimPrefix(prefix, "./")
	if prefix == "." {
		prefix = ""
	}
	kind := drilldownStringArg(args, "warning_kind")
	construct := drilldownStringArg(args, "construct")
	rule := drilldownStringArg(args, "rule_id")
	warnings = slices.DeleteFunc(warnings, func(w coverageWarning) bool {
		return rule != "" && w.RuleID != rule || kind != "" && string(w.Kind) != kind || construct != "" && w.Construct != construct || prefix != "" && w.File != prefix && !strings.HasPrefix(w.File, prefix+"/")
	})
	slices.SortFunc(warnings, func(a, b coverageWarning) int {
		return cmp.Or(strings.Compare(a.File, b.File), cmp.Compare(a.StartLine, b.StartLine), cmp.Compare(a.StartColumn, b.StartColumn), strings.Compare(string(a.Kind), string(b.Kind)), strings.Compare(a.Construct, b.Construct), strings.Compare(a.RuleID, b.RuleID), strings.Compare(a.ScanID, b.ScanID), strings.Compare(a.Message, b.Message))
	})
	offset := min(max(drilldownIntArg(args, "offset", 0), 0), len(warnings))
	limit := drilldownIntArg(args, "limit", 20)
	if limit <= 0 {
		limit = 20
	}
	limit = min(limit, 100)
	end := min(offset+limit, len(warnings))
	locations := make([]coverageLocation, 0, end-offset)
	for _, w := range warnings[offset:end] {
		locations = append(locations, coverageLocation{Message: runeclamp.Fit(w.Message, 320), MessageTruncated: len([]rune(w.Message)) > 320, ScanID: w.ScanID, Kind: w.Kind, Path: w.File, Line: w.StartLine, Column: w.StartColumn, Construct: w.Construct, RuleID: w.RuleID})
	}
	scopeWarnings := make([]api.ScanWarning, len(warnings))
	for i, warning := range warnings {
		scopeWarnings[i] = warning.ScanWarning
	}
	var next *int
	if end < len(warnings) {
		next = &end
	}
	return marshalDrilldownJSON(struct {
		ScanIDs    []string           `json:"scan_ids"`
		Scope      scancoverage.Scope `json:"scope"`
		Locations  []coverageLocation `json:"locations"`
		TotalMatch int                `json:"total_match"`
		Offset     int                `json:"offset"`
		NextOffset *int               `json:"next_offset,omitempty"`
		Truncated  bool               `json:"truncated"`
	}{ids, scancoverage.Summarize(scopeWarnings), locations, len(warnings), offset, next, next != nil})
}
