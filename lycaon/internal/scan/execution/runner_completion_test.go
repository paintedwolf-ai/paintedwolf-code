package execution

import (
	"context"
	"database/sql"
	"testing"

	"github.com/lycaon/lycaon/internal/events"
	scanbase "github.com/lycaon/lycaon/internal/scan"
	scancfg "github.com/lycaon/lycaon/internal/scan/configuration"
	scanfindings "github.com/lycaon/lycaon/internal/scan/findings"
	scanoutput "github.com/lycaon/lycaon/internal/scan/output"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type completionOutbox struct{ committed func() }

func (*completionOutbox) EnqueueTx(context.Context, *sql.Tx, api.EventTopic, events.PublishKey, any) error {
	return nil
}

func (o *completionOutbox) Notify() { o.committed() }

func TestRunnerCompletionSurvivesLeaseRetirementAfterCommit(t *testing.T) {
	store := scanbase.NewSQLStore(testdbfixture.Open(t, "scan.db"))
	finding := scanfindings.FixtureFinding("new-rule", api.FindingLevelHigh, "new finding", "main.go", 1)
	scanner := &scanbase.MockScanner{Result: &scanoutput.Result{FindingsCount: 1, Findings: []api.SecurityFinding{finding}}}
	ingester := &scanbase.IngesterImpl{Module: scancfg.DefaultModuleConfig()}
	runner := NewRunner(store, &scanbase.MockRegistry{Scanner: scanner}, ingester, scancfg.DefaultRunnerConfig(), nil)
	runner.Snapshots = testSnapshots(t, store)
	root := t.TempDir()
	writeScanSource(t, root, "main.go", "package main\n")
	testutil.FailErr(t, "insert scan", store.Insert(t.Context(), api.CodeScan{
		ID: "complete-before-retirement", CanonicalPath: root, ScannerID: scanner.ID(),
		Status: api.CodeScanStatusPending, Categories: scanner.Categories(),
		SourceSnapshotID: publishTestSnapshot(t, runner.Snapshots, root),
	}, nil, ""))
	job, err := store.ClaimNext(t.Context())
	testutil.FailErr(t, "claim scan", err)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	store.SetEventOutbox(&completionOutbox{committed: func() {
		committed, err := store.Get(t.Context(), job.ID)
		testutil.FailErr(t, "read committed transition", err)
		if committed != nil && committed.Status == api.CodeScanStatusComplete {
			cancel()
		}
	}})
	var terminal api.CodeScan
	runner.OnTerminal = func(ctx context.Context, scan api.CodeScan) {
		testutil.FailErr(t, "terminal context", ctx.Err())
		terminal = scan
	}
	runner.execute(ctx, job)
	if ctx.Err() == nil {
		t.Fatal("completion did not retire the execution context")
	}
	if terminal.Status != api.CodeScanStatusComplete || terminal.CompletedAt == nil || terminal.StartedAt == nil {
		t.Fatalf("terminal notification lost committed metadata: %+v", terminal)
	}
	open, err := store.OpenFindings(t.Context(), root, scanner.ID())
	testutil.FailErr(t, "read finding history", err)
	if len(open) != 1 {
		t.Fatalf("completion lost finding history: %+v", open)
	}
}
