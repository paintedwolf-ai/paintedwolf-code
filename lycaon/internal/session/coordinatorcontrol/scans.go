package coordinatorcontrol

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/loopwake"
	"github.com/lycaon/lycaon/pkg/api"
)

type ScanSessions interface {
	WorkspaceNotificationIDs(context.Context, string, string) ([]string, error)
}
type ScanEvidenceRuns interface {
	ActiveRunOwnsScanEvidence(context.Context, string) bool
}
type Scans struct {
	Sessions ScanSessions
	Runtime  *coordinator.Runtime
	Wait     ScanWaitState
	Evidence ScanEvidenceRuns
}

const scanDeltaNoteLimit = 8

type ScanWaitState struct {
	// InFlight reports whether a scan or full pass the session requested is unfinished.
	InFlight func(ctx context.Context, sessionID string) bool
	// Requested reports whether the session asked for the scan.
	Requested func(ctx context.Context, sessionID, scanID string) bool
}

func (m *Scans) Finished(ctx context.Context, scan api.CodeScan) {
	if m == nil || m.Wait.Requested == nil {
		return
	}
	loop := m.Runtime.CoordinatorLoop()
	for _, sessionID := range loop.Waits.SessionsSleepingOn(loopwake.WaitTriggerScanDone) {
		if !m.Wait.Requested(ctx, sessionID, scan.ID) {
			continue
		}
		loop.Nudges.NudgeScanFinished(ctx, sessionID, scan.ID, anchor.Envelope{
			ScanID:            scan.ID,
			ScanStatus:        string(scan.Status),
			ScanCategories:    scanCategoriesLabel(scan.Categories),
			ScanFindingsCount: scan.FindingsCount,
		})
	}
}
func scanCategoriesLabel(categories []api.ScanCategory) string {
	parts := make([]string, 0, len(categories))
	for _, c := range categories {
		if s := strings.TrimSpace(string(c)); s != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, ", ")
}
func (m *Scans) Delta(ctx context.Context, scan api.CodeScan, introduced, fixed []api.SecurityFinding) {
	if m == nil || m.Sessions == nil || strings.TrimSpace(scan.CanonicalPath) == "" || (len(introduced) == 0 && len(fixed) == 0) {
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
	rt := m.Runtime
	var after string
	for {
		ids, err := m.Sessions.WorkspaceNotificationIDs(ctx, scan.CanonicalPath, after)
		if err != nil || len(ids) == 0 {
			return
		}
		for _, id := range ids {
			if m.Evidence != nil && m.Evidence.ActiveRunOwnsScanEvidence(ctx, id) {
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
