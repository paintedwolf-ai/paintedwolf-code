package toolapi_test

import (
	"context"
	"encoding/json"
	"testing"

	scanbase "github.com/lycaon/lycaon/internal/scan"
	scancadence "github.com/lycaon/lycaon/internal/scan/cadence"
	scancfg "github.com/lycaon/lycaon/internal/scan/configuration"
	scanfindings "github.com/lycaon/lycaon/internal/scan/findings"
	scantoolapi "github.com/lycaon/lycaon/internal/scan/toolapi"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestScanCompareToolDiffsFindings(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "tool.db")

	store := scanbase.NewSQLStore(sqlDB)
	coord := newTestCoordinator(t, store, nil)
	projectDir := t.TempDir()

	oldID := seedCompareScan(t, store, compareSeedOpts{
		projectDir: projectDir,
		findings: []api.SecurityFinding{
			scanfindings.FixtureFinding("gone", api.FindingLevelHigh, "", "a.go", 1),
		},
	})
	newID := seedCompareScan(t, store, compareSeedOpts{
		projectDir: projectDir,
	})

	reg := tools.NewDefaultRegistry()
	testutil.FailErr(t, "RegisterScanTools", scantoolapi.RegisterScanTools(reg, coord, &scanbase.MockRegistry{Scanner: &scanbase.MockScanner{}}, scancadence.New(scanbase.StoreFromCoordinator(coord), coord, &scanbase.MockRegistry{Scanner: &scanbase.MockScanner{}}, nil, scancfg.DefaultGatesConfig(), nil), nil, nil))

	raw, err := reg.Run(context.Background(), "scan_compare", map[string]any{
		"old_scan_id": oldID,
		"new_scan_id": newID,
	}, scanToolContext("sess-1", projectDir))
	testutil.FailErr(t, "scan_compare tool", err)

	var resp scanbase.Comparison
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		testutil.FailErr(t, "unmarshal compare response", err)
	}
	if resp.ResolvedCount != 1 || resp.NewCount != 0 {
		t.Fatalf("compare = %+v", resp)
	}
}

func TestScanCompareToolAcceptsShortBoardIDs(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "tool-short.db")

	store := scanbase.NewSQLStore(sqlDB)
	coord := newTestCoordinator(t, store, nil)
	projectDir := t.TempDir()

	oldID := seedCompareScan(t, store, compareSeedOpts{
		projectDir: projectDir,
		findings: []api.SecurityFinding{
			scanfindings.FixtureFinding("gone", api.FindingLevelHigh, "", "a.go", 1),
		},
	})
	newID := seedCompareScan(t, store, compareSeedOpts{
		projectDir: projectDir,
	})

	reg := tools.NewDefaultRegistry()
	testutil.FailErr(t, "RegisterScanTools", scantoolapi.RegisterScanTools(reg, coord, &scanbase.MockRegistry{Scanner: &scanbase.MockScanner{}}, scancadence.New(scanbase.StoreFromCoordinator(coord), coord, &scanbase.MockRegistry{Scanner: &scanbase.MockScanner{}}, nil, scancfg.DefaultGatesConfig(), nil), nil, nil))

	raw, err := reg.Run(context.Background(), "scan_compare", map[string]any{
		"old_scan_id": oldID[:8],
		"new_scan_id": newID[:8],
	}, scanToolContext("sess-1", projectDir))
	testutil.FailErr(t, "scan_compare short ids", err)

	var resp scanbase.Comparison
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		testutil.FailErr(t, "unmarshal compare response", err)
	}
	if resp.OldScanID != oldID || resp.NewScanID != newID {
		t.Fatalf("ids = old %q new %q want old %q new %q", resp.OldScanID, resp.NewScanID, oldID, newID)
	}
}

func TestScanCompareToolAutoBaselineOmitsOldScanID(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "tool-auto.db")

	store := scanbase.NewSQLStore(sqlDB)
	coord := newTestCoordinator(t, store, nil)
	projectDir := t.TempDir()

	oldID := seedCompareScan(t, store, compareSeedOpts{
		projectDir: projectDir,
		findings: []api.SecurityFinding{
			scanfindings.FixtureFinding("gone", api.FindingLevelHigh, "", "a.go", 1),
		},
	})
	newID := seedCompareScan(t, store, compareSeedOpts{
		projectDir: projectDir,
	})

	reg := tools.NewDefaultRegistry()
	testutil.FailErr(t, "RegisterScanTools", scantoolapi.RegisterScanTools(reg, coord, &scanbase.MockRegistry{Scanner: &scanbase.MockScanner{}}, scancadence.New(scanbase.StoreFromCoordinator(coord), coord, &scanbase.MockRegistry{Scanner: &scanbase.MockScanner{}}, nil, scancfg.DefaultGatesConfig(), nil), nil, nil))

	raw, err := reg.Run(context.Background(), "scan_compare", map[string]any{
		"new_scan_id": newID,
	}, scanToolContext("sess-1", projectDir))
	testutil.FailErr(t, "scan_compare auto baseline", err)

	var resp scanbase.Comparison
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		testutil.FailErr(t, "unmarshal compare response", err)
	}
	if resp.OldScanID != oldID || resp.NewScanID != newID {
		t.Fatalf("ids = old %q new %q want old %q new %q", resp.OldScanID, resp.NewScanID, oldID, newID)
	}
	if resp.ResolvedCount != 1 || resp.NewCount != 0 {
		t.Fatalf("compare = %+v", resp)
	}
}
