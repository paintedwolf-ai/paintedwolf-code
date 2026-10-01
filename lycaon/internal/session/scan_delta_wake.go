package session

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/pkg/api"
)

// scanDeltaNoteLimit bounds the findings a delta note lists inline.
const scanDeltaNoteLimit = 8

// ScanEvidenceRuns says whether a session's active workflow run brings its
// own scan evidence.
type ScanEvidenceRuns interface {
	ActiveRunOwnsScanEvidence(ctx context.Context, sessionID string) bool
}

// SetScanEvidenceRuns wires the run check that keeps ambient scan notes out of
// runs with bound scans.
func (m *Manager) SetScanEvidenceRuns(runs ScanEvidenceRuns) {
	if m != nil {
		m.scanEvidenceRuns = runs
	}
}

// NoteScanDelta queues scan findings for each session's next safe boundary. A
// session whose workflow run binds its own scans is skipped: an ambient delta
// would stand in for the run's evidence.
func (m *Manager) NoteScanDelta(ctx context.Context, scan api.CodeScan, introduced, fixed []api.SecurityFinding) {
	if m == nil || m.store == nil || strings.TrimSpace(scan.CanonicalPath) == "" || (len(introduced) == 0 && len(fixed) == 0) {
		return
	}
	data := map[string]any{
		"scan_id":                  scan.ID,
		"scanner_id":               scan.ScannerID,
		"introduced_count":         len(introduced),
		"fixed_count":              len(fixed),
		"introduced":               scanDeltaNoteRows(introduced),
		"fixed":                    scanDeltaNoteRows(fixed),
		"introduced_omitted_count": max(0, len(introduced)-scanDeltaNoteLimit),
		"fixed_omitted_count":      max(0, len(fixed)-scanDeltaNoteLimit),
	}
	rt := m.ensureCoordinatorRuntime()
	var after string
	for {
		ids, err := m.store.WorkspaceNotificationIDs(ctx, scan.CanonicalPath, after)
		if err != nil || len(ids) == 0 {
			return
		}
		for _, id := range ids {
			if m.scanEvidenceRuns != nil && m.scanEvidenceRuns.ActiveRunOwnsScanEvidence(ctx, id) {
				continue
			}
			rt.Anchors().Emit(ctx, id, anchor.ScanDelta, anchor.Envelope{ScanID: scan.ID, Vars: data})
		}
		after = ids[len(ids)-1]
	}
}

func scanDeltaNoteRows(findings []api.SecurityFinding) []map[string]any {
	rows := make([]map[string]any, 0, min(len(findings), scanDeltaNoteLimit))
	for i, finding := range findings {
		if i >= scanDeltaNoteLimit {
			break
		}
		row := map[string]any{"rule_id": finding.RuleID, "level": string(finding.Level), "message": finding.Message}
		if len(finding.Locations) > 0 {
			row["path"] = finding.Locations[0].URI
			row["line"] = finding.Locations[0].StartLine
		}
		rows = append(rows, row)
	}
	return rows
}
