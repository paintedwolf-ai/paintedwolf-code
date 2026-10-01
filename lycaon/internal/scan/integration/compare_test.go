package integration

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/advisory"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/scan"
	scancfg "github.com/lycaon/lycaon/internal/scan/configuration"
	scanfindings "github.com/lycaon/lycaon/internal/scan/findings"
	scanoutput "github.com/lycaon/lycaon/internal/scan/output"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type compareSeedOpts struct {
	projectDir   string
	categories   []api.ScanCategory
	delegationID string
	findings     []api.SecurityFinding
	rawCount     int
}

func seedCompareScan(t *testing.T, store *scan.SQLStore, opts compareSeedOpts) string {
	t.Helper()
	if opts.projectDir == "" {
		opts.projectDir = t.TempDir()
	}
	if len(opts.categories) == 0 {
		opts.categories = []api.ScanCategory{api.ScanCategorySecurity}
	}
	coord := newTestCoordinator(t, store, nil)
	created, err := coord.Enqueue(context.Background(), scan.EnqueueRequest{
		ProjectDir:   opts.projectDir,
		Categories:   opts.categories,
		DelegationID: opts.delegationID,
	})
	testutil.FailErr(t, "coord.Enqueue failed", err)
	rawCount := opts.rawCount
	if rawCount == 0 {
		rawCount = len(opts.findings)
	}
	claimed := claimScan(t, store, created.ID)
	if _, err := store.MarkComplete(context.Background(), claimed, &scanoutput.Result{
		FindingsCount: rawCount,
		Findings:      opts.findings,
	}); err != nil {
		testutil.FailErr(t, "store.MarkComplete failed", err)
	}
	artifacts := map[string]any{
		"findings":        opts.findings,
		"guidance":        []api.ScanGuidanceSummary{},
		"findings_count":  rawCount,
		"findings_stored": len(opts.findings),
	}
	if err := store.SaveIngest(context.Background(), created.ID, evidence.Record{
		GateVerdict: string(evidence.GateVerdictPassed),
		Artifacts:   artifacts,
	}); err != nil {
		testutil.FailErr(t, "store.SaveIngest failed", err)
	}
	return created.ID
}

func newCompareFixture(t *testing.T) (*scan.SQLStore, *scan.CoordinatorImpl, string) {
	t.Helper()
	sqlDB := testdbfixture.Open(t, "compare.db")
	store := scan.NewSQLStore(sqlDB)
	coord := newTestCoordinator(t, store, nil)
	return store, coord, t.TempDir()
}

func TestCompareNewResolvedPersisted(t *testing.T) {
	_, coord, projectDir := newCompareFixture(t)
	store := coord.Store

	oldID := seedCompareScan(t, store, compareSeedOpts{
		projectDir: projectDir,
		findings: []api.SecurityFinding{
			scanfindings.FixtureFinding("old-only", api.FindingLevelHigh, "gone", "gone.go", 1),
			scanfindings.FixtureFinding("shared", api.FindingLevelMedium, "stay", "stay.go", 2),
		},
	})
	newID := seedCompareScan(t, store, compareSeedOpts{
		projectDir: projectDir,
		findings: []api.SecurityFinding{
			scanfindings.FixtureFinding("shared", api.FindingLevelMedium, "stay", "stay.go", 2),
			scanfindings.FixtureFinding("new-only", api.FindingLevelCritical, "fresh", "fresh.go", 3),
		},
	})

	resp, err := coord.Compare(context.Background(), oldID, newID)
	testutil.FailErr(t, "coord.Compare failed", err)
	if resp.NewCount != 1 || resp.ResolvedCount != 1 || resp.PersistedCount != 1 {
		t.Fatalf("counts = new:%d resolved:%d persisted:%d", resp.NewCount, resp.ResolvedCount, resp.PersistedCount)
	}
	if len(resp.NewFindings) != 1 || resp.NewFindings[0].RuleID != "new-only" {
		t.Fatalf("new findings = %#v", resp.NewFindings)
	}
	if len(resp.ResolvedFindings) != 1 || resp.ResolvedFindings[0].RuleID != "old-only" {
		t.Fatalf("resolved findings = %#v", resp.ResolvedFindings)
	}
	if len(resp.PersistedFindings) != 1 || resp.PersistedFindings[0].RuleID != "shared" {
		t.Fatalf("persisted findings = %#v", resp.PersistedFindings)
	}
	if resp.NewByLevel[string(api.FindingLevelCritical)] != 1 {
		t.Fatalf("new_by_level = %#v", resp.NewByLevel)
	}
	if resp.ResolvedByLevel[string(api.FindingLevelHigh)] != 1 {
		t.Fatalf("resolved_by_level = %#v", resp.ResolvedByLevel)
	}
}

func TestCompareSCAAdvisoryPersistedDifferentPath(t *testing.T) {
	_, coord, projectDir := newCompareFixture(t)
	store := coord.Store
	cve := "CVE-2024-7777"
	oldFinding := scanfindings.BuildSecurityFinding(scanfindings.FindingBuildOpts{
		DriverID: "trivy",
		RuleID:   advisory.RuleID(advisory.BuildAdvisoryRef(cve)),
		Level:    api.FindingLevelHigh,
		Kind:     api.FindingKindSCA,
		Locations: []api.SecurityFindingLocation{{
			URI: "go.mod",
		}},
		Advisory: advisory.BuildAdvisoryRef(cve),
	})
	newFinding := scanfindings.BuildSecurityFinding(scanfindings.FindingBuildOpts{
		DriverID: "lycaon-sca",
		RuleID:   advisory.RuleID(advisory.BuildAdvisoryRef(cve)),
		Level:    api.FindingLevelHigh,
		Kind:     api.FindingKindSCA,
		Locations: []api.SecurityFindingLocation{{
			URI: "vendor/foo/go.mod",
		}},
		Advisory: advisory.BuildAdvisoryRef(cve),
	})
	if oldFinding.Fingerprints.Primary == newFinding.Fingerprints.Primary {
		t.Fatal("fixture must use different primary fingerprints")
	}

	oldID := seedCompareScan(t, store, compareSeedOpts{projectDir: projectDir, findings: []api.SecurityFinding{oldFinding}})
	newID := seedCompareScan(t, store, compareSeedOpts{projectDir: projectDir, findings: []api.SecurityFinding{newFinding}})

	resp, err := coord.Compare(context.Background(), oldID, newID)
	testutil.FailErr(t, "coord.Compare failed", err)
	if resp.NewCount != 0 || resp.ResolvedCount != 0 || resp.PersistedCount != 1 {
		t.Fatalf("counts = new:%d resolved:%d persisted:%d", resp.NewCount, resp.ResolvedCount, resp.PersistedCount)
	}
	if len(resp.PersistedFindings) != 1 || scanfindings.PrimaryURI(resp.PersistedFindings[0]) != "vendor/foo/go.mod" {
		t.Fatalf("persisted = %#v", resp.PersistedFindings)
	}
}

func TestComparePartialFixInOneFile(t *testing.T) {
	_, coord, projectDir := newCompareFixture(t)
	store := coord.Store

	oldID := seedCompareScan(t, store, compareSeedOpts{
		projectDir: projectDir,
		findings: []api.SecurityFinding{
			scanfindings.FixtureFinding("rule-a", api.FindingLevelHigh, "line 1", "pkg/a.go", 1),
			scanfindings.FixtureFinding("rule-b", api.FindingLevelHigh, "line 2", "pkg/a.go", 2),
		},
	})
	newID := seedCompareScan(t, store, compareSeedOpts{
		projectDir: projectDir,
		findings: []api.SecurityFinding{
			scanfindings.FixtureFinding("rule-b", api.FindingLevelHigh, "line 2", "pkg/a.go", 2),
		},
	})

	resp, err := coord.Compare(context.Background(), oldID, newID)
	testutil.FailErr(t, "coord.Compare failed", err)
	if resp.NewCount != 0 || resp.ResolvedCount != 1 || resp.PersistedCount != 1 {
		t.Fatalf("counts = new:%d resolved:%d persisted:%d", resp.NewCount, resp.ResolvedCount, resp.PersistedCount)
	}
	if resp.ResolvedFindings[0].RuleID != "rule-a" || resp.PersistedFindings[0].RuleID != "rule-b" {
		t.Fatalf("resolved=%#v persisted=%#v", resp.ResolvedFindings, resp.PersistedFindings)
	}
}

func TestCompareTruncatedOnlyAtPresentationLimit(t *testing.T) {
	_, coord, projectDir := newCompareFixture(t)
	store := coord.Store
	oldID := seedCompareScan(t, store, compareSeedOpts{projectDir: projectDir})
	findings := make([]api.SecurityFinding, 0, scancfg.DefaultAgentBudget().MaxGuidanceFindings+1)
	for idx := 0; idx <= scancfg.DefaultAgentBudget().MaxGuidanceFindings; idx++ {
		findings = append(findings, scanfindings.FixtureFinding(
			fmt.Sprintf("rule-%d", idx), api.FindingLevelHigh, "", fmt.Sprintf("file-%d.go", idx), 1,
		))
	}
	newID := seedCompareScan(t, store, compareSeedOpts{
		projectDir: projectDir,
		findings:   findings,
	})

	resp, err := coord.Compare(context.Background(), oldID, newID)
	testutil.FailErr(t, "coord.Compare failed", err)
	if !resp.Truncated {
		t.Fatal("expected truncated=true when comparison presentation was capped")
	}
	if resp.NewCount != len(findings) || len(resp.NewFindings) != scancfg.DefaultAgentBudget().MaxGuidanceFindings {
		t.Fatalf("new count/list = %d/%d", resp.NewCount, len(resp.NewFindings))
	}
}

func TestCompareEmptyScans(t *testing.T) {
	_, coord, projectDir := newCompareFixture(t)
	store := coord.Store

	oldID := seedCompareScan(t, store, compareSeedOpts{projectDir: projectDir})
	newID := seedCompareScan(t, store, compareSeedOpts{projectDir: projectDir})

	resp, err := coord.Compare(context.Background(), oldID, newID)
	testutil.FailErr(t, "coord.Compare failed", err)
	if resp.NewCount != 0 || resp.ResolvedCount != 0 || resp.PersistedCount != 0 {
		t.Fatalf("counts = %+v", resp)
	}
	if len(resp.NewFindings) != 0 || len(resp.ResolvedFindings) != 0 || len(resp.PersistedFindings) != 0 {
		t.Fatalf("non-empty buckets on empty scans: %+v", resp)
	}
}

func TestCompareRejectSameScan(t *testing.T) {
	_, coord, projectDir := newCompareFixture(t)
	id := seedCompareScan(t, coord.Store, compareSeedOpts{projectDir: projectDir})

	_, err := coord.Compare(context.Background(), id, id)
	var reject *scan.CompareReject
	if !errors.As(err, &reject) || reject.Code != scan.CompareRejectSameScan {
		t.Fatalf("err = %v want %s", err, scan.CompareRejectSameScan)
	}
}

func TestCompareRejectProjectMismatch(t *testing.T) {
	_, coord, _ := newCompareFixture(t)
	store := coord.Store

	oldID := seedCompareScan(t, store, compareSeedOpts{projectDir: t.TempDir()})
	newID := seedCompareScan(t, store, compareSeedOpts{projectDir: t.TempDir()})

	_, err := coord.Compare(context.Background(), oldID, newID)
	var reject *scan.CompareReject
	if !errors.As(err, &reject) || reject.Code != scan.CompareRejectProjectMismatch {
		t.Fatalf("err = %v want %s", err, scan.CompareRejectProjectMismatch)
	}
}

func TestCompareSortsNewFindingsCriticalFirst(t *testing.T) {
	_, coord, projectDir := newCompareFixture(t)
	store := coord.Store

	oldID := seedCompareScan(t, store, compareSeedOpts{projectDir: projectDir})
	newID := seedCompareScan(t, store, compareSeedOpts{
		projectDir: projectDir,
		findings: []api.SecurityFinding{
			scanfindings.FixtureFinding("low", api.FindingLevelLow, "", "a.go", 1),
			scanfindings.FixtureFinding("crit", api.FindingLevelCritical, "", "b.go", 2),
			scanfindings.FixtureFinding("med", api.FindingLevelMedium, "", "c.go", 3),
		},
	})

	resp, err := coord.Compare(context.Background(), oldID, newID)
	testutil.FailErr(t, "coord.Compare failed", err)
	if resp.NewCount != 3 || len(resp.NewFindings) != 3 {
		t.Fatalf("new = %#v", resp)
	}
	if resp.NewFindings[0].RuleID != "crit" || resp.NewFindings[1].RuleID != "med" || resp.NewFindings[2].RuleID != "low" {
		t.Fatalf("order = %#v", resp.NewFindings)
	}
}

func TestPreviousCompleteDelegationPreferred(t *testing.T) {
	_, coord, projectDir := newCompareFixture(t)
	store := coord.Store
	categories := []api.ScanCategory{api.ScanCategorySecurity, api.ScanCategorySecret}

	first := seedCompareScan(t, store, compareSeedOpts{
		projectDir:   projectDir,
		categories:   categories,
		delegationID: "dep-1",
	})
	time.Sleep(10 * time.Millisecond)
	second := seedCompareScan(t, store, compareSeedOpts{
		projectDir:   projectDir,
		categories:   categories,
		delegationID: "dep-1",
	})
	latest, err := store.Get(context.Background(), second)
	testutil.FailErr(t, "store.Get failed", err)

	got, err := coord.PreviousComplete(context.Background(), *latest)
	testutil.FailErr(t, "coord.PreviousComplete failed", err)
	if got == nil || got.ID != first {
		t.Fatalf("previous = %#v want %s", got, first)
	}
}

func TestPreviousCompleteProjectFallbackSkipsOtherCategories(t *testing.T) {
	_, coord, projectDir := newCompareFixture(t)
	store := coord.Store

	securityOld := seedCompareScan(t, store, compareSeedOpts{
		projectDir: projectDir,
		categories: []api.ScanCategory{api.ScanCategorySecurity},
	})
	time.Sleep(10 * time.Millisecond)
	_ = seedCompareScan(t, store, compareSeedOpts{
		projectDir: projectDir,
		categories: []api.ScanCategory{api.ScanCategorySecret},
	})
	time.Sleep(10 * time.Millisecond)
	securityLatest := seedCompareScan(t, store, compareSeedOpts{
		projectDir: projectDir,
		categories: []api.ScanCategory{api.ScanCategorySecurity},
	})

	latest, err := store.Get(context.Background(), securityLatest)
	testutil.FailErr(t, "store.Get failed", err)

	got, err := coord.PreviousComplete(context.Background(), *latest)
	testutil.FailErr(t, "coord.PreviousComplete failed", err)
	if got == nil || got.ID != securityOld {
		t.Fatalf("previous = %#v want %s", got, securityOld)
	}
}

func TestPreviousCompleteNilWhenOnlyScan(t *testing.T) {
	_, coord, projectDir := newCompareFixture(t)
	store := coord.Store

	only := seedCompareScan(t, store, compareSeedOpts{projectDir: projectDir})
	latest, err := store.Get(context.Background(), only)
	testutil.FailErr(t, "store.Get failed", err)

	got, err := coord.PreviousComplete(context.Background(), *latest)
	testutil.FailErr(t, "coord.PreviousComplete failed", err)
	if got != nil {
		t.Fatalf("previous = %#v want nil", got)
	}
}
