package security

import (
	"context"
	"fmt"
	"maps"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/wiring"
)

func TestAPIScanPackBundledFindingsE2E(t *testing.T) {
	projectDir := scanFixtureDir(t)
	if _, err := os.Stat(projectDir); err != nil {
		testutil.FailErr(t, "scan fixture dir", err)
	}

	t.Run("secret_category_single_engine", func(t *testing.T) {
		h := wiring.BuildForTest(t, wiring.WithBundledScanners())
		cancel := h.StartBackgroundWorkers(t, context.Background())
		t.Cleanup(cancel)

		payload := waitScanPackComplete(t, h, projectDir, []any{string(wire.ScanCategorySecret)}, 60*time.Second)
		if payload.Status != wire.CodeScanStatusComplete {
			t.Fatalf("status = %q message=%q err=%q", payload.Status, payload.Message, payload.Error)
		}
		if len(payload.ScanIDs) != 1 {
			t.Fatalf("scan_ids = %v, want one secrets engine", payload.ScanIDs)
		}
		if payload.FindingsCount < 1 {
			t.Fatalf("aggregated findings_count = %d, want >= 1", payload.FindingsCount)
		}
		project := createProjectHTTP(t, h.Server, projectDir)
		assertScanPackPerEngine(t, h.Server, project.ID, payload, map[string]engineScanExpect{
			"lycaon-secrets": {minFindings: 1, wantFile: "secrets.env"},
		})
	})

	t.Run("all_categories_fan_out_and_aggregate", func(t *testing.T) {
		if testing.Short() {
			t.Skip("bundled scan_pack all-categories E2E skipped in -short")
		}

		h := wiring.BuildForTest(t, wiring.WithBundledScanners())
		// Clear test-mode scanner suppression.
		t.Setenv("LYCAON_TEST", "")
		skipIfOpenGrepUnavailable(t)
		cancel := h.StartBackgroundWorkers(t, context.Background())
		t.Cleanup(cancel)

		payload := waitScanPackComplete(t, h, projectDir, []any{"all"}, bundledScannerWaitBudget)
		if payload.Status != wire.CodeScanStatusComplete {
			t.Fatalf("status = %q message=%q err=%q", payload.Status, payload.Message, payload.Error)
		}
		if payload.FindingsCount < 2 {
			t.Fatalf("aggregated findings_count = %d, want >= 2 (secret + sca or sast)", payload.FindingsCount)
		}
		project := createProjectHTTP(t, h.Server, projectDir)
		assertScanPackPerEngine(t, h.Server, project.ID, payload, map[string]engineScanExpect{
			"lycaon-secrets": {minFindings: 1, wantFile: "secrets.env"},
			"lycaon-sca":     {minFindings: 1, wantFile: "package-lock.json"},
			"lycaon-sast":    {minFindings: 1, wantFile: "vuln.go"},
		})
	})
}

func TestAPIScanPackMatchesPerCategoryAPIRuns(t *testing.T) {
	if testing.Short() {
		t.Skip("scan_pack vs API parity skipped in -short")
	}

	projectDir := scanFixtureDir(t)
	h := wiring.BuildForTest(t, wiring.WithBundledScanners())
	t.Setenv("LYCAON_TEST", "")
	skipIfOpenGrepUnavailable(t)
	cancel := h.StartBackgroundWorkers(t, context.Background())
	t.Cleanup(cancel)

	perCategory := map[string]bool{}
	var projectID string
	for _, tc := range []struct {
		category wire.ScanCategory
		engine   string
	}{
		{wire.ScanCategorySecret, "lycaon-secrets"},
		{wire.ScanCategorySCA, "lycaon-sca"},
		{wire.ScanCategorySAST, "lycaon-sast"},
	} {
		created := enqueueCodeScan(t, h.Server, projectDir, []wire.ScanCategory{tc.category})
		projectID = created.ProjectID
		got := waitScanComplete(t, h.Server, created.ProjectID, created.ID)
		if got.Status != wire.CodeScanStatusComplete {
			t.Fatalf("category %s status = %q error=%q", tc.category, got.Status, got.Error)
		}
		if got.ScannerID != tc.engine {
			t.Fatalf("category %s scanner_id = %q, want %q", tc.category, got.ScannerID, tc.engine)
		}
		for key := range findingKeys(got) {
			perCategory[key] = true
		}
	}
	if len(perCategory) == 0 {
		t.Fatal("per-category scans of the fixture reported no findings")
	}

	payload := waitScanPackComplete(t, h, projectDir, []any{"all"}, bundledScannerWaitBudget)
	if payload.Status != wire.CodeScanStatusComplete {
		t.Fatalf("scan_pack status = %q error=%q", payload.Status, payload.Error)
	}
	if len(payload.ScanIDs) != 3 {
		t.Fatalf("scan_ids = %v, want 3 engine rows", payload.ScanIDs)
	}
	pack := map[string]bool{}
	for _, id := range payload.ScanIDs {
		for key := range findingKeys(getCodeScan(t, h.Server, projectID, id)) {
			pack[key] = true
		}
	}
	if !maps.Equal(pack, perCategory) {
		t.Fatalf("scan_pack findings differ from the per-category scans:\npack:         %v\nper category: %v", slices.Sorted(maps.Keys(pack)), slices.Sorted(maps.Keys(perCategory)))
	}
}

// findingKeys identifies each finding by scanner, rule and first location.
func findingKeys(scan wire.CodeScan) map[string]bool {
	keys := map[string]bool{}
	for _, f := range scan.Findings {
		key := scan.ScannerID + " " + f.RuleID
		if len(f.Locations) > 0 {
			key += fmt.Sprintf(" %s:%d", f.Locations[0].URI, f.Locations[0].StartLine)
		}
		keys[key] = true
	}
	return keys
}
