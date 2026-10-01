package scan

import (
	"encoding/json"
	"github.com/lycaon/lycaon/internal/db"
	scanignore "github.com/lycaon/lycaon/internal/scan/ignores"
	scanoutput "github.com/lycaon/lycaon/internal/scan/output"
	"github.com/lycaon/lycaon/pkg/api"
)

// codeScansFromRows maps generated rows onto the wire type.
func codeScansFromRows(rows []db.CodeScans) []api.CodeScan {
	out := make([]api.CodeScan, 0, len(rows))
	for _, r := range rows {
		out = append(out, *codeScanFromRow(r))
	}
	return out
}

// codeScanFromRow maps a generated code_scans row onto the wire type.
func codeScanFromRow(r db.CodeScans) *api.CodeScan {
	scan := api.CodeScan{
		ID:                r.ID,
		CanonicalPath:     r.CanonicalPath,
		Status:            api.CodeScanStatus(r.Status),
		DelegationID:      r.DelegationID,
		HeadSHA:           r.HeadSha,
		SourceSnapshotID:  r.SourceSnapshotID,
		ReplacementScanID: r.ReplacementScanID,
		ScannerID:         db.StringFromNull(r.ScannerID),
		ClaimedBy:         db.StringFromNull(r.ClaimedBy),
		ClaimToken:        db.StringFromNull(r.ClaimToken),
		Attempt:           int(r.Attempt),
	}
	if r.RuntimeJson != "" {
		var policy api.ScanRuntimePolicy
		if err := json.Unmarshal([]byte(r.RuntimeJson), &policy); err == nil {
			scan.Runtime = &policy
		}
	}
	if r.ProgressJson != "" {
		var progress api.ScanProgress
		if err := json.Unmarshal([]byte(r.ProgressJson), &progress); err == nil {
			scan.Progress = &progress
		}
	}
	if r.DeltaJson != "" {
		var delta api.ScanDelta
		if err := json.Unmarshal([]byte(r.DeltaJson), &delta); err == nil {
			scan.Delta = &delta
		}
	}
	if r.StartedAt.Valid {
		if t, err := db.ParseTime(r.StartedAt.String); err == nil {
			scan.StartedAt = &t
		}
	}
	if r.LongRunningAt.Valid {
		if t, err := db.ParseTime(r.LongRunningAt.String); err == nil {
			scan.LongRunningAt = &t
			scan.LongRunning = true
		}
	}
	_ = json.Unmarshal([]byte(r.CategoriesJson), &scan.Categories)
	if r.ResultJson.Valid {
		scan.Result = json.RawMessage(r.ResultJson.String)
		scan.FindingsCount = findingsCountFromResult(scan.Result)
	}
	if r.GuidanceJson.Valid && r.GuidanceJson.String != "" {
		_ = json.Unmarshal([]byte(r.GuidanceJson.String), &scan.Guidance)
	}
	if r.IngestJson.Valid && r.IngestJson.String != "" {
		var stored IngestArtifacts
		if err := json.Unmarshal([]byte(r.IngestJson.String), &stored); err == nil {
			scan.Findings = stored.Findings
			scan.Ignored = scanignore.ToAPIIgnored(stored.Ignored)
			if stored.FindingsCount > 0 {
				scan.FindingsCount = stored.FindingsCount
			}
			scan.FindingsStored = stored.FindingsStored
			if scan.FindingsStored == 0 && len(stored.Findings) > 0 {
				scan.FindingsStored = len(stored.Findings)
			}
			scan.FindingsMerged = stored.FindingsMerged
			scan.FindingsByLevel = stored.FindingsByLevel
			scan.AgentBudget = stored.AgentBudget
			scan.ScanScope = stored.ScanScope
			scan.Warnings = stored.Warnings
		}
	}
	scan.SupportedQueryFilters = api.DefaultSupportedQueryFilters()
	scan.Trigger = api.ScanTrigger(r.Trigger)
	scan.Error = db.StringFromNull(r.Error)
	scan.CreatedAt, _ = db.ParseTime(r.CreatedAt)
	if r.CompletedAt.Valid {
		if t, err := db.ParseTime(r.CompletedAt.String); err == nil {
			scan.CompletedAt = &t
		}
	}
	if r.HeartbeatAt.Valid {
		if t, err := db.ParseTime(r.HeartbeatAt.String); err == nil {
			scan.HeartbeatAt = &t
		}
	}
	if r.LeaseExpiresAt.Valid {
		if t, err := db.ParseTime(r.LeaseExpiresAt.String); err == nil {
			scan.LeaseExpiresAt = &t
		}
	}
	return &scan
}

func findingsCountFromResult(raw json.RawMessage) int {
	if len(raw) == 0 {
		return 0
	}
	var result scanoutput.Result
	if err := json.Unmarshal(raw, &result); err != nil {
		return 0
	}
	return result.FindingsCount
}
