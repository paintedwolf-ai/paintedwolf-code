package toolapi_test

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	scanbase "github.com/lycaon/lycaon/internal/scan"
	scanoutput "github.com/lycaon/lycaon/internal/scan/output"
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

func seedCompareScan(t *testing.T, store *scanbase.SQLStore, opts compareSeedOpts) string {
	t.Helper()
	if opts.projectDir == "" {
		opts.projectDir = t.TempDir()
	}
	if len(opts.categories) == 0 {
		opts.categories = []api.ScanCategory{api.ScanCategorySecurity}
	}
	coord := newTestCoordinator(t, store, nil)
	created, err := coord.Enqueue(context.Background(), scanbase.EnqueueRequest{
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
