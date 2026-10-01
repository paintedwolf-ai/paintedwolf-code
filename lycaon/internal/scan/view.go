package scan

import (
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
)

// ParseScanView normalizes the scan HTTP/MCP view parameter. Empty means summary.
func ParseScanView(view string) (string, error) {
	v := strings.ToLower(strings.TrimSpace(view))
	if v == "" {
		return "summary", nil
	}
	if v == "summary" || v == "full" {
		return v, nil
	}
	return "", fmt.Errorf("invalid view %q: use summary or full", view)
}

// ApplyScanView trims response fields for a validated view (summary or full).
func ApplyScanView(scan *api.CodeScan, view string) *api.CodeScan {
	if scan == nil {
		return nil
	}
	out := *scan
	if out.Warnings != nil {
		out.WarningSummary = SummarizeWarnings(out.Warnings)
	}
	switch view {
	case "full":
		out.Result = nil
		return &out
	default:
		out.Result = nil
		out.Findings = nil
		out.Ignored = nil
		out.Warnings = nil
		return &out
	}
}
