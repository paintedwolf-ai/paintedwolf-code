package command

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/observability/cmddiag"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/pkg/api"
)

type stubDiagReader struct {
	pages    []api.CommandProgramReceiptPage
	pageErr  error
	pins     map[string][]toolcontract.ExecutionRevision
	pinErr   error
	blockCh  chan struct{}
	entered  chan struct{}
	reads    atomic.Int64
	pinReads atomic.Int64
}

func (s *stubDiagReader) ReadProgramPage(ctx context.Context, _, _, cursor string, _ int) (api.CommandProgramReceiptPage, error) {
	s.reads.Add(1)
	if s.entered != nil {
		select {
		case <-s.entered:
		default:
			close(s.entered)
		}
	}
	if s.blockCh != nil {
		select {
		case <-s.blockCh:
		case <-ctx.Done():
			return api.CommandProgramReceiptPage{}, ctx.Err()
		}
	}
	if s.pageErr != nil {
		return api.CommandProgramReceiptPage{}, s.pageErr
	}
	idx := 0
	switch cursor {
	case "p2":
		idx = 1
	case "p3":
		idx = 2
	}
	if idx < len(s.pages) {
		return s.pages[idx], nil
	}
	return api.CommandProgramReceiptPage{}, nil
}

func (s *stubDiagReader) RevisionPins(_ context.Context, _, executionID string) ([]toolcontract.ExecutionRevision, error) {
	s.pinReads.Add(1)
	if s.pinErr != nil {
		return nil, s.pinErr
	}
	return s.pins[executionID], nil
}

func diagIntPtr(v int) *int { return &v }

func settledReceiptPage(programID string, rev int64, execs ...api.CommandExecutionReceipt) api.CommandProgramReceiptPage {
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	return api.CommandProgramReceiptPage{
		Program: api.CommandProgramReceipt{
			ID:               programID,
			ParentReceiptID:  "rcpt-" + programID,
			State:            "completed",
			StatusCode:       diagIntPtr(0),
			SettledAt:        &now,
			ActivityRevision: rev,
		},
		Executions: execs,
	}
}

func TestDiagnosticsBlockedReaderDoesNotBlockCaller(t *testing.T) {
	blockCh := make(chan struct{})
	entered := make(chan struct{})
	reader := &stubDiagReader{blockCh: blockCh, entered: entered}

	obs := cmddiag.NewObserver(cmddiag.Config{Enabled: true, QueueCapacity: 2, TaskTimeout: 5 * time.Second, CloseGrace: 50 * time.Millisecond})
	t.Cleanup(obs.Close)

	start := time.Now()
	if !observeSettledProgramDiagnostics(obs, reader, "sess-1", "call-1", "prog-blocked", nil) {
		t.Fatal("expected task to enqueue immediately")
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("caller blocked waiting for reader: %v", elapsed)
	}
	<-entered
	// Close cancels the worker context so the blocked reader unblocks cleanly.
	obs.Close()
}

func TestDiagnosticsOverflowPerformsZeroReceiptReads(t *testing.T) {
	blockCh := make(chan struct{})
	var closeOnce sync.Once
	unblock := func() { closeOnce.Do(func() { close(blockCh) }) }
	entered := make(chan struct{})
	blocker := &stubDiagReader{blockCh: blockCh, entered: entered}
	overflowReader := &stubDiagReader{}

	var mu sync.Mutex
	var findings []cmddiag.Finding
	obs := cmddiag.NewObserver(cmddiag.Config{
		Enabled:       true,
		QueueCapacity: 1,
		Sink: func(f cmddiag.Finding) {
			mu.Lock()
			findings = append(findings, f)
			mu.Unlock()
		},
	})
	t.Cleanup(func() {
		unblock()
		obs.Close()
	})

	if !observeSettledProgramDiagnostics(obs, blocker, "sess-1", "call-1", "prog-1", nil) {
		t.Fatal("blocker task should enqueue")
	}
	<-entered
	// Fill queue slot 1.
	if !observeSettledProgramDiagnostics(obs, blocker, "sess-1", "call-2", "prog-2", nil) {
		t.Fatal("second task should fill queue")
	}
	// Overflow tasks must drop before touching overflowReader.
	for i := 0; i < 4; i++ {
		if observeSettledProgramDiagnostics(obs, overflowReader, "sess-1", "call-ovf", "prog-ovf", nil) {
			t.Fatal("expected overflow task to be dropped")
		}
	}
	unblock()
	obs.Close()
	if overflowReader.reads.Load() != 0 || overflowReader.pinReads.Load() != 0 {
		t.Fatalf("overflow performed receipt reads: pages=%d pins=%d", overflowReader.reads.Load(), overflowReader.pinReads.Load())
	}
	mu.Lock()
	defer mu.Unlock()
	sawOverflow := false
	for _, f := range findings {
		if f.RuleID == cmddiag.RuleCaptureOverflow && f.Disposition == cmddiag.DispositionOverflow && f.DroppedCount == 4 {
			sawOverflow = true
		}
	}
	if !sawOverflow {
		t.Fatalf("expected capture_overflow with dropped_count=4, got %+v", findings)
	}
}

func TestDiagnosticsWiringCatchesStaleDeliveryAndDeduplicates(t *testing.T) {
	reader := &stubDiagReader{
		pages: []api.CommandProgramReceiptPage{
			settledReceiptPage("prog-stale", 4, api.CommandExecutionReceipt{
				ID: "exec-1", ProgramID: "prog-stale", Owner: "native", NativeCommand: "printf",
				State: "completed", OwnerStarted: true, OwnerInvoked: true, StatusCode: diagIntPtr(0),
			}),
		},
	}
	// Same comparable revision (rev 4) with stale non-terminal state contradicts.
	staleDelivered := &api.CommandProgramReceipt{
		ID:               "prog-stale",
		State:            "running",
		ActivityRevision: 4,
		Activities:       []api.CommandActivity{{ID: "act-1", State: "running", ExecutionIDs: []string{"exec-1"}}},
	}
	// Earlier host callback revision (rev 2 < rev 4) is normal async revision drift -> incomplete.
	driftedDelivered := &api.CommandProgramReceipt{
		ID:               "prog-drift",
		State:            "running",
		ActivityRevision: 2,
	}
	driftReader := &stubDiagReader{
		pages: []api.CommandProgramReceiptPage{settledReceiptPage("prog-drift", 4)},
	}

	var mu sync.Mutex
	var got []cmddiag.Finding
	obs := cmddiag.NewObserver(cmddiag.Config{
		Enabled:       true,
		QueueCapacity: 8,
		Sink: func(f cmddiag.Finding) {
			mu.Lock()
			got = append(got, f)
			mu.Unlock()
		},
	})
	t.Cleanup(obs.Close)

	observeSettledProgramDiagnostics(obs, reader, "sess-11112222", "call-1", "prog-stale", staleDelivered)
	observeSettledProgramDiagnostics(obs, reader, "sess-11112222", "call-1", "prog-stale", staleDelivered)
	observeSettledProgramDiagnostics(obs, driftReader, "sess-11112222", "call-2", "prog-drift", driftedDelivered)
	obs.Close()

	mu.Lock()
	defer mu.Unlock()
	if len(got) != 2 {
		t.Fatalf("expected 2 findings (1 deduped contradiction + 1 drift incomplete), got %d: %+v", len(got), got)
	}
	if got[0].RuleID != cmddiag.RuleStaleTerminalPresentation || got[0].Disposition != cmddiag.DispositionContradiction {
		t.Fatalf("unexpected stale finding: %+v", got[0])
	}
	if got[1].RuleID != cmddiag.RuleStaleTerminalPresentation || got[1].Disposition != cmddiag.DispositionIncomplete || got[1].Reason != "host_snapshot_revision_drift" {
		t.Fatalf("unexpected drift finding: %+v", got[1])
	}
}

func TestDiagnosticsWiringDispositionsForPagesPinsAndPartialEffects(t *testing.T) {
	t.Run("capped_pages_report_incomplete_not_contradiction", func(t *testing.T) {
		p1 := settledReceiptPage("prog-cap", 1, api.CommandExecutionReceipt{ID: "e1", State: "completed"})
		p1.NextCursor, p1.ExecutionsOmitted = "p2", 2
		p2 := settledReceiptPage("prog-cap", 1, api.CommandExecutionReceipt{ID: "e2", State: "completed"})
		p2.NextCursor, p2.ExecutionsOmitted = "p3", 1
		p3 := settledReceiptPage("prog-cap", 1, api.CommandExecutionReceipt{ID: "e3", State: "completed"})
		p3.NextCursor, p3.ExecutionsOmitted = "p4", 10
		reader := &stubDiagReader{pages: []api.CommandProgramReceiptPage{p1, p2, p3}}
		delivered := &cmddiag.PresentationSnapshot{ProgramState: "completed", ProgramStatusCode: diagIntPtr(0), ActivityRevision: 1, HostBoundaryOnly: true}
		fetched, err := fetchProgramObservation(t.Context(), reader, "s1", "c1", "prog-cap", delivered)
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		findings := cmddiag.Evaluate(fetched, time.Now())
		if len(findings) != 1 || findings[0].RuleID != cmddiag.RuleMissingChildTerminal || findings[0].Disposition != cmddiag.DispositionIncomplete {
			t.Fatalf("expected incomplete for capped pages, got %+v", findings)
		}
	})

	t.Run("inconsistent_pages_and_pin_lookup_loss_retain_ids", func(t *testing.T) {
		p1 := settledReceiptPage("prog-inc", 1, api.CommandExecutionReceipt{
			ID: "e1", State: "completed", EffectCertainty: "completed", PublishedRevision: "rev-1",
		})
		p1.NextCursor, p1.ExecutionsOmitted = "p2", 1
		p2 := settledReceiptPage("prog-inc", 1, api.CommandExecutionReceipt{ID: "e1", State: "completed"})
		reader := &stubDiagReader{pages: []api.CommandProgramReceiptPage{p1, p2}, pinErr: errors.New("db busy")}
		delivered := &cmddiag.PresentationSnapshot{ProgramState: "completed", ProgramStatusCode: diagIntPtr(0), ActivityRevision: 1, HostBoundaryOnly: true}
		fetched, err := fetchProgramObservation(t.Context(), reader, "s1", "c1", "prog-inc", delivered)
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		findings := cmddiag.Evaluate(fetched, time.Now())
		if len(findings) != 1 || findings[0].RuleID != cmddiag.RuleObservationLoss || findings[0].Disposition != cmddiag.DispositionIncomplete {
			t.Fatalf("expected observation_loss incomplete, got %+v", findings)
		}
		if len(findings[0].ExecutionIDs) != 1 || findings[0].ExecutionIDs[0] != "e1" {
			t.Fatalf("expected failed lookup execution ID retained, got %+v", findings[0].ExecutionIDs)
		}
	})

	t.Run("completed_mutation_discloses_unsupported_with_hashed_source_ref", func(t *testing.T) {
		page := settledReceiptPage("prog-mut", 1,
			api.CommandExecutionReceipt{ID: "e-refused", State: "refused"},
			api.CommandExecutionReceipt{ID: "e-write", NativeCommand: "write", State: "completed", EffectCertainty: "completed", PublishedRevision: "rev-2"},
		)
		reader := &stubDiagReader{
			pages: []api.CommandProgramReceiptPage{page},
			pins: map[string][]toolcontract.ExecutionRevision{
				"e-write": {{Role: toolcontract.DestinationRevision, RootID: "/secret/root", Path: "sub/secret_file.go"}},
			},
		}
		delivered := &cmddiag.PresentationSnapshot{ProgramState: "completed", ProgramStatusCode: diagIntPtr(0), ActivityRevision: 1, HostBoundaryOnly: true}
		fetched, err := fetchProgramObservation(t.Context(), reader, "s1", "c1", "prog-mut", delivered)
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		findings := cmddiag.Evaluate(fetched, time.Now())
		if len(findings) != 1 || findings[0].RuleID != cmddiag.RuleOperationSourceContradiction || findings[0].Disposition != cmddiag.DispositionUnsupported {
			t.Fatalf("expected unsupported op/source disclosure, got %+v", findings)
		}
		if len(findings[0].SourceIDs) != 1 || !strings.HasPrefix(findings[0].SourceIDs[0], "ref:") {
			t.Fatalf("expected opaque hashed source ref, got %+v", findings[0].SourceIDs)
		}
	})
}

func TestDiagnosticsOverflowLargeActivityListIsBounded(t *testing.T) {
	manyActs := make([]api.CommandActivity, 500)
	manyIDs := make([]string, 20)
	for i := range manyIDs {
		manyIDs[i] = "exec-item"
	}
	for i := range manyActs {
		manyActs[i] = api.CommandActivity{ID: "act-item", State: "completed", ExecutionIDs: manyIDs}
	}
	largeReceipt := api.CommandProgramReceipt{
		ID:               "prog-large",
		State:            "completed",
		StatusCode:       diagIntPtr(0),
		ActivityRevision: 5,
		Activities:       manyActs,
	}

	snap, copied := boundedPresentationFromReceipt(largeReceipt)
	if copied != cmddiag.DefaultMaxSnapshotActivities || len(snap.Activities) != cmddiag.DefaultMaxSnapshotActivities {
		t.Fatalf("expected bounded copy of %d activities, got copied=%d len=%d", cmddiag.DefaultMaxSnapshotActivities, copied, len(snap.Activities))
	}
	if !snap.Truncated || snap.ActivityCount != 500 || !snap.HostBoundaryOnly {
		t.Fatalf("expected Truncated=true ActivityCount=500 HostBoundaryOnly=true, got %+v", snap)
	}
	if len(snap.Activities[0].ExecutionIDs) != cmddiag.DefaultMaxSnapshotExecIDs {
		t.Fatalf("expected bounded exec IDs %d, got %d", cmddiag.DefaultMaxSnapshotExecIDs, len(snap.Activities[0].ExecutionIDs))
	}

	// Truncated host snapshot must report incomplete rather than silently clean.
	evalFindings := cmddiag.Evaluate(cmddiag.Observation{
		ProgramID:         "prog-large",
		ProgramState:      "completed",
		ProgramStatusCode: diagIntPtr(0),
		ProgramSettled:    true,
		ActivityRevision:  5,
		Presentation:      snap,
	}, time.Now())
	if len(evalFindings) != 1 || evalFindings[0].RuleID != cmddiag.RuleStaleTerminalPresentation || evalFindings[0].Disposition != cmddiag.DispositionIncomplete {
		t.Fatalf("expected incomplete for truncated snapshot, got %+v", evalFindings)
	}

	// Under queue saturation, large receipt drops with zero ProgramReader calls.
	blockCh := make(chan struct{})
	var closeOnce sync.Once
	unblock := func() { closeOnce.Do(func() { close(blockCh) }) }
	entered := make(chan struct{})
	blocker := &stubDiagReader{blockCh: blockCh, entered: entered}
	overflowReader := &stubDiagReader{}
	obs := cmddiag.NewObserver(cmddiag.Config{Enabled: true, QueueCapacity: 1})
	t.Cleanup(func() { unblock(); obs.Close() })

	observeSettledProgramDiagnostics(obs, blocker, "s1", "c1", "prog-b1", nil)
	<-entered
	observeSettledProgramDiagnostics(obs, blocker, "s1", "c2", "prog-b2", nil)
	if observeSettledProgramDiagnostics(obs, overflowReader, "s1", "c3", "prog-large", &largeReceipt) {
		t.Fatal("expected large receipt submission to drop on full queue")
	}
	unblock()
	obs.Close()
	if overflowReader.reads.Load() != 0 || overflowReader.pinReads.Load() != 0 {
		t.Fatalf("overflow performed receipt reads: pages=%d pins=%d", overflowReader.reads.Load(), overflowReader.pinReads.Load())
	}
}
