package security

import (
	"context"
	"encoding/json"
	"testing"

	wire "github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/wiring"
)

func TestAPIScanPackNativeQueryE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("scan_pack + native query E2E skipped in -short")
	}

	projectDir := scanFixtureDir(t)
	h := wiring.BuildForTest(t, wiring.WithBundledScanners())
	t.Setenv("LYCAON_TEST", "")
	skipIfOpenGrepUnavailable(t)
	cancel := h.StartBackgroundWorkers(t, context.Background())
	t.Cleanup(cancel)

	payload := waitScanPackComplete(t, h, projectDir, []any{"all"}, bundledScannerWaitBudget)
	if payload.Status != wire.CodeScanStatusComplete {
		t.Fatalf("scan_pack status = %q error=%q", payload.Status, payload.Error)
	}
	if len(payload.PerScan) < 3 {
		t.Fatalf("per_scan rows = %d, want 3", len(payload.PerScan))
	}
	if payload.FindingsCount < 1 {
		t.Fatalf("findings_count = %d, want >= 1", payload.FindingsCount)
	}

	var scaScanID string
	for _, row := range payload.PerScan {
		if row.ScannerID == "lycaon-sca" && row.FindingsCount > 0 {
			scaScanID = row.ScanID
			break
		}
	}
	if scaScanID == "" {
		t.Fatalf("no SCA row with findings in per_scan: %+v", payload.PerScan)
	}

	raw, err := h.ToolRegistry.Run(context.Background(), "scan_query", map[string]any{
		"scan_ids": []any{scaScanID},
		"limit":    5,
	}, securityToolContext("", projectDir, "coordinator"))
	if err != nil {
		t.Fatalf("scan_query: %v", err)
	}
	var resp struct {
		Findings []struct {
			RuleID string `json:"rule_id"`
		} `json:"findings"`
	}
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		t.Fatalf("decode query response: %v body=%s", err, raw)
	}
	if len(resp.Findings) == 0 {
		t.Fatalf("expected findings rows, got %s", raw)
	}

	var sastScanID string
	for _, row := range payload.PerScan {
		if row.ScannerID == "lycaon-sast" && row.FindingsCount > 0 {
			sastScanID = row.ScanID
			break
		}
	}
	if sastScanID == "" {
		t.Fatalf("no SAST row with findings in per_scan: %+v", payload.PerScan)
	}
	raw, err = h.ToolRegistry.Run(context.Background(), "scan_query", map[string]any{
		"scan_ids": []any{sastScanID},
		"limit":    10,
	}, securityToolContext("", projectDir, "coordinator"))
	if err != nil {
		t.Fatalf("scan_query SAST: %v", err)
	}
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		t.Fatalf("decode SAST query response: %v body=%s", err, raw)
	}
	if len(resp.Findings) == 0 {
		t.Fatalf("expected SAST findings rows, got %s", raw)
	}
}
