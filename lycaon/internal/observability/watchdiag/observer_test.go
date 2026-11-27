package watchdiag

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/debugpaths"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestDisabledByDefaultHasZeroSideEffects(t *testing.T) {
	t.Setenv("LYCAON_DEV_WATCH_DIAGNOSTICS", "")
	t.Setenv(debugpaths.FullDebugEnv, "")
	outFile := filepath.Join(t.TempDir(), "watch-diagnostics.jsonl")
	t.Setenv("LYCAON_DEV_WATCH_DIAGNOSTICS_FILE", outFile)
	Close()
	t.Cleanup(Close)

	if Enabled() {
		t.Fatal("expected dev watch diagnostics disabled by default")
	}
	base := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	obs := cleanWatchObservation(base)
	obs.PublicationPhases = nil
	if Observe(obs) {
		t.Fatal("expected Observe to return false when disabled")
	}
	if rep := Report(); rep.TotalFindings != 0 || len(rep.Findings) != 0 {
		t.Fatalf("expected empty report when disabled, got %+v", rep)
	}
	if _, err := os.Stat(outFile); !os.IsNotExist(err) {
		t.Fatalf("expected no diagnostics file when disabled, stat err=%v", err)
	}
}

func TestDeduplicatesAndBoundsLocalReport(t *testing.T) {
	base := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	obs := NewObserver(Config{
		Enabled:        true,
		QueueCapacity:  32,
		ReportCapacity: 3,
		Now:            func() time.Time { return base },
	})

	// Submit the same contradiction 3 times -> deduplicated to 1.
	dup := cleanWatchObservation(base)
	dup.WatchID = "watch-dup"
	dup.Deliveries[0].WatchID = "watch-dup"
	dup.Deliveries[0].RoutedSessionID = "99999999-8888-7777-6666-555555555555"
	obs.Submit(dup)
	obs.Submit(dup)
	obs.Submit(dup)

	// Submit 4 distinct watch contradictions so report cap (3) evicts oldest.
	for i := 0; i < 4; i++ {
		distinct := cleanWatchObservation(base)
		id := fmt.Sprintf("watch-cap-%d", i)
		distinct.WatchID = id
		distinct.Deliveries[0].WatchID = id
		distinct.Deliveries[0].RoutedSessionID = "99999999-8888-7777-6666-555555555555"
		obs.Submit(distinct)
	}
	obs.Close()

	rep := obs.Report()
	if rep.Capacity != 3 {
		t.Fatalf("report capacity = %d, want 3", rep.Capacity)
	}
	if rep.TotalFindings != 3 || len(rep.Findings) != 3 {
		t.Fatalf("expected bounded report of 3 findings, got total=%d len=%d", rep.TotalFindings, len(rep.Findings))
	}
	if rep.DeduplicatedCount != 2 {
		t.Fatalf("deduplicated_count = %d, want 2", rep.DeduplicatedCount)
	}
	if rep.EvictedCount != 2 {
		t.Fatalf("evicted_count = %d, want 2", rep.EvictedCount)
	}
	if rep.Findings[0].WatchID != "watch-cap-1" || rep.Findings[2].WatchID != "watch-cap-3" {
		t.Fatalf("expected newest 3 findings retained, got %q..%q", rep.Findings[0].WatchID, rep.Findings[2].WatchID)
	}
}

func TestBoundedQueueOverflowRecordsDropsWithoutBlocking(t *testing.T) {
	base := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	gate := make(chan struct{})
	var closeGateOnce sync.Once
	closeGate := func() { closeGateOnce.Do(func() { close(gate) }) }
	firstEntered := make(chan struct{})
	var mu sync.Mutex
	var got []Finding

	obs := NewObserver(Config{
		Enabled:       true,
		QueueCapacity: 2,
		Now:           func() time.Time { return base },
		Sink: func(f Finding) {
			if f.RuleID == RuleTerminalDeliveryBeforeReport {
				select {
				case <-firstEntered:
				default:
					close(firstEntered)
				}
				<-gate
			}
			mu.Lock()
			got = append(got, f)
			mu.Unlock()
		},
	})
	t.Cleanup(func() {
		closeGate()
		obs.Close()
	})

	bad := cleanWatchObservation(base)
	bad.PublicationPhases = nil

	if !obs.Submit(bad) {
		t.Fatal("first submit should enqueue")
	}
	<-firstEntered

	dropped := 0
	for i := 0; i < 10; i++ {
		if !obs.Submit(bad) {
			dropped++
		}
	}
	if dropped == 0 {
		t.Fatal("expected bounded queue to drop observations under saturation")
	}

	closeGate()
	obs.Close()

	if stats := obs.Stats(); stats.Dropped != int64(dropped) {
		t.Fatalf("stats.Dropped = %d, want %d", stats.Dropped, dropped)
	}

	mu.Lock()
	defer mu.Unlock()
	sawOverflow := false
	for _, f := range got {
		if f.RuleID == RuleCaptureOverflow && f.Disposition == DispositionOverflow && f.DroppedCount == int64(dropped) {
			sawOverflow = true
		}
	}
	if !sawOverflow {
		t.Fatalf("expected capture_overflow finding with dropped_count=%d, got %+v", dropped, got)
	}
}

func TestSubmitTaskOverflowPerformsZeroFetchesAndErrorsEmitObservationLoss(t *testing.T) {
	base := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	block := make(chan struct{})
	var closeBlockOnce sync.Once
	closeBlock := func() { closeBlockOnce.Do(func() { close(block) }) }
	entered := make(chan struct{})

	obs := NewObserver(Config{Enabled: true, QueueCapacity: 1})
	t.Cleanup(func() {
		closeBlock()
		obs.Close()
	})

	if !obs.SubmitTask(Task{
		WatchID: "watch-blocker",
		Fetch: func(ctx context.Context) (Observation, error) {
			close(entered)
			select {
			case <-block:
			case <-ctx.Done():
			}
			return cleanWatchObservation(base), nil
		},
	}) {
		t.Fatal("blocker task should enqueue")
	}
	<-entered

	// Fill capacity=1 with a failing fetch task.
	if !obs.SubmitTask(Task{
		SessionID: "11111111-2222-3333-4444-555555555555",
		WatchID:   "watch-err",
		Handle:    "bg-err",
		Fetch: func(context.Context) (Observation, error) {
			return Observation{}, errors.New("store read failed")
		},
	}) {
		t.Fatal("error task should fill queue")
	}

	var overflowFetches atomic.Int64
	for i := 0; i < 4; i++ {
		if obs.SubmitTask(Task{
			WatchID: "watch-overflow",
			Fetch: func(context.Context) (Observation, error) {
				overflowFetches.Add(1)
				return cleanWatchObservation(base), nil
			},
		}) {
			t.Fatal("expected task to drop while queue is full")
		}
	}
	closeBlock()
	obs.Close()

	if overflowFetches.Load() != 0 {
		t.Fatalf("expected 0 fetches for dropped tasks, got %d", overflowFetches.Load())
	}
	rep := obs.Report()
	sawLoss := false
	for _, f := range rep.Findings {
		if f.RuleID == RuleObservationLoss && f.Disposition == DispositionIncomplete && f.WatchID == "watch-err" {
			sawLoss = true
		}
	}
	if !sawLoss {
		t.Fatalf("expected observation_loss finding in local report, got %+v", rep.Findings)
	}
}

func TestRedactionScrubsSecretsAndDigestsPaths(t *testing.T) {
	const bearer = "Bearer sk-ant-api03-verysecretvalue1234567890"
	const apiKey = "OPENAI_API_KEY=sk-1234567890abcdef1234567890"
	const pathCanary = "/Users/dev/secret_workspace_canary/internal/private_watch.go"

	digest := DigestReference("/Users/dev/secret_workspace_canary", "internal/private_watch.go")
	if !strings.HasPrefix(digest, "ref:") || len(digest) != 16 || strings.Contains(digest, "secret") {
		t.Fatalf("unexpected digest format: %q", digest)
	}

	outFile := filepath.Join(t.TempDir(), "watch-diagnostics.jsonl")
	t.Setenv("LYCAON_DEV_WATCH_DIAGNOSTICS", "1")
	t.Setenv("LYCAON_DEV_WATCH_DIAGNOSTICS_FILE", outFile)
	Close()
	t.Cleanup(Close)

	base := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	obs := cleanWatchObservation(base)
	obs.PublicationPhases = nil
	obs.Deliveries[0].EvidenceRef = pathCanary
	obs.Deliveries[0].Handle = "handle?" + bearer + "&" + apiKey
	if !Observe(obs) {
		t.Fatal("expected Observe to accept observation when enabled")
	}
	Close()

	raw, err := os.ReadFile(outFile)
	testutil.FailErr(t, "read watch diagnostics jsonl", err)
	body := string(raw)
	for _, forbidden := range []string{"sk-ant-api03", "sk-1234567890abcdef", "secret_workspace_canary", "private_watch.go"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("watch diagnostics output leaked %q: %s", forbidden, body)
		}
	}
	var decoded Finding
	lines := strings.Split(strings.TrimSpace(body), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected 1 finding line, got %d: %s", len(lines), body)
	}
	testutil.FailErr(t, "decode watch finding jsonl", json.Unmarshal([]byte(lines[0]), &decoded))
	if decoded.RuleID != RuleTerminalDeliveryBeforeReport || decoded.Disposition != DispositionContradiction {
		t.Fatalf("unexpected decoded finding: %+v", decoded)
	}
}

type workloadCounts struct {
	deliveriesCommitted    int
	presentationsCommitted int
	turnsAdmitted          int
	turnsBlockedByStop     int
	workUnitsExecuted      int
}

func runSyntheticWatchWorkload(base time.Time, diagEnabled bool) (workloadCounts, LocalReport) {
	obs := NewObserver(Config{
		Enabled:        diagEnabled,
		QueueCapacity:  16,
		ReportCapacity: 16,
		Now:            func() time.Time { return base },
	})
	restore := SetObserver(obs)
	defer func() {
		restore()
		obs.Close()
	}()

	var counts workloadCounts
	const stopEpoch int64 = 3
	for i := 0; i < 6; i++ {
		counts.workUnitsExecuted++
		snapshot := cleanWatchObservation(base)
		snapshot.WatchID = fmt.Sprintf("watch-parity-%d", i)
		snapshot.Deliveries[0].WatchID = snapshot.WatchID
		snapshot.Presentations[0].WatchID = snapshot.WatchID
		snapshot.StopFence.StopEpoch = stopEpoch

		// Product decision: admit watch turn only strictly before Stop fence.
		turnEpoch := int64(i + 1)
		if turnEpoch < stopEpoch {
			counts.deliveriesCommitted++
			counts.presentationsCommitted++
			counts.turnsAdmitted++
			snapshot.AdmittedTurns[0].StopEpoch = turnEpoch
			snapshot.AdmittedTurns[0].Admitted = true
		} else {
			counts.turnsBlockedByStop++
			snapshot.AdmittedTurns[0].StopEpoch = turnEpoch
			snapshot.AdmittedTurns[0].Admitted = false
		}
		Observe(snapshot)
	}
	obs.Close()
	return counts, obs.Report()
}

func TestEnabledAndDisabledParityByCount(t *testing.T) {
	base := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)

	disabledCounts, disabledReport := runSyntheticWatchWorkload(base, false)
	enabledCounts, enabledReport := runSyntheticWatchWorkload(base, true)

	if disabledCounts != enabledCounts {
		t.Fatalf("enabled vs disabled parity mismatch: disabled=%+v enabled=%+v", disabledCounts, enabledCounts)
	}
	if disabledReport.TotalFindings != 0 || enabledReport.TotalFindings != 0 {
		t.Fatalf("expected 0 findings on clean parity workload, disabled=%+v enabled=%+v", disabledReport, enabledReport)
	}
}
