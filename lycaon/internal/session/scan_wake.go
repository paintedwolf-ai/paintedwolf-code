package session

import (
	"context"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/loopwake"
	"github.com/lycaon/lycaon/pkg/api"
	"strings"
)

// ScanWaitState answers the scan questions a scan_done wait asks about one session.
// Both concern only scans the session requested: an automatic scan of the same
// tree neither holds the wait open nor ends it.
type ScanWaitState struct {
	// InFlight reports whether a scan or full pass the session requested is unfinished.
	InFlight func(ctx context.Context, sessionID string) bool
	// Requested reports whether the session asked for the scan.
	Requested func(ctx context.Context, sessionID, scanID string) bool
}

// SetScanWaitState wires the scan store lookups behind wait() scan_done.
func (m *Manager) SetScanWaitState(state ScanWaitState) {
	if m == nil {
		return
	}
	m.scanWaits = state
}

// NudgeCoordinatorScanDone wakes coordinators sleeping on a scan_done subscription
// that asked for the finished scan. Sessions that did not request it are never
// nudged, so automatic scans cannot spam a waiting coordinator.
func (m *Manager) NudgeCoordinatorScanDone(ctx context.Context, scan api.CodeScan) {
	if m == nil || m.scanWaits.Requested == nil {
		return
	}
	loop := m.ensureCoordinatorRuntime().CoordinatorLoop()
	for _, sessionID := range loop.Subscriptions.SessionsSleepingOn(loopwake.WaitTriggerScanDone) {
		if !m.scanWaits.Requested(ctx, sessionID, scan.ID) {
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
