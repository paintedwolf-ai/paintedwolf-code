package cmddiag

import (
	"context"
	"encoding/json"
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
	t.Setenv("LYCAON_DEV_COMMAND_DIAGNOSTICS", "")
	t.Setenv(debugpaths.FullDebugEnv, "")
	outFile := filepath.Join(t.TempDir(), "command-diagnostics.jsonl")
	t.Setenv("LYCAON_DEV_COMMAND_DIAGNOSTICS_FILE", outFile)
	Close()
	t.Cleanup(Close)

	if Enabled() {
		t.Fatal("expected dev command diagnostics disabled by default")
	}
	obs := cleanSettledObservation()
	obs.Presentation.ProgramState = "running"
	if submitObservation(DefaultObserver(), obs) {
		t.Fatal("expected observer to return false when disabled")
	}
	if _, err := os.Stat(outFile); !os.IsNotExist(err) {
		t.Fatalf("expected no diagnostics file when disabled, stat err=%v", err)
	}
}

func TestBoundedQueueOverflowRecordsDropsWithoutBlocking(t *testing.T) {
	gate := make(chan struct{})
	var closeGateOnce sync.Once
	closeGate := func() { closeGateOnce.Do(func() { close(gate) }) }
	firstEntered := make(chan struct{})
	var mu sync.Mutex
	var got []Finding

	obs := NewObserver(Config{
		Enabled:       true,
		QueueCapacity: 2,
		Now:           func() time.Time { return time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC) },
		Sink: func(f Finding) {
			if f.RuleID == RuleStaleTerminalPresentation {
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

	bad := cleanSettledObservation()
	bad.Presentation.ProgramState = "running"

	if !submitObservation(obs, bad) {
		t.Fatal("first submit should enqueue")
	}
	<-firstEntered

	dropped := 0
	for i := 0; i < 10; i++ {
		if !submitObservation(obs, bad) {
			dropped++
		}
	}
	if dropped == 0 {
		t.Fatal("expected bounded queue to drop observations under saturation")
	}

	closeGate()
	obs.Close()

	stats := obs.stats()
	if stats.Dropped != int64(dropped) {
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
		t.Fatalf("expected explicit capture_overflow finding with dropped_count=%d, got %+v", dropped, got)
	}
}

func TestSubmitTaskOverflowPerformsZeroFetches(t *testing.T) {
	block := make(chan struct{})
	var closeBlockOnce sync.Once
	closeBlock := func() { closeBlockOnce.Do(func() { close(block) }) }
	entered := make(chan struct{})

	obs := NewObserver(Config{Enabled: true, QueueCapacity: 1})
	t.Cleanup(func() {
		closeBlock()
		obs.Close()
	})

	// Occupy the single worker goroutine.
	if !obs.SubmitTask(Task{
		ProgramID: "prog-blocker",
		Fetch: func(ctx context.Context) (Observation, error) {
			close(entered)
			select {
			case <-block:
			case <-ctx.Done():
			}
			return cleanSettledObservation(), nil
		},
	}) {
		t.Fatal("blocker task should enqueue")
	}
	<-entered

	// Fill queue capacity=1.
	if !submitObservation(obs, cleanSettledObservation()) {
		t.Fatal("second item should fill queue")
	}

	// Any further task must drop immediately without ever invoking Fetch.
	var overflowFetches atomic.Int64
	for i := 0; i < 5; i++ {
		if obs.SubmitTask(Task{
			ProgramID: "prog-overflow",
			Fetch: func(context.Context) (Observation, error) {
				overflowFetches.Add(1)
				return cleanSettledObservation(), nil
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
	if obs.stats().Dropped != 5 {
		t.Fatalf("stats.Dropped = %d, want 5", obs.stats().Dropped)
	}
}

func TestDeduplicatesFindingsByRuleAndProgram(t *testing.T) {
	var mu sync.Mutex
	var got []Finding
	obs := NewObserver(Config{
		Enabled:       true,
		QueueCapacity: 8,
		Sink: func(f Finding) {
			mu.Lock()
			got = append(got, f)
			mu.Unlock()
		},
	})
	bad := cleanSettledObservation()
	bad.ProgramID = "prog-dup-check"
	bad.Presentation.ProgramState = "running"

	submitObservation(obs, bad)
	submitObservation(obs, bad)
	submitObservation(obs, bad)
	obs.Close()

	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 {
		t.Fatalf("expected 1 deduplicated finding, got %d: %+v", len(got), got)
	}
	if obs.stats().Deduplicated != 2 {
		t.Fatalf("stats.Deduplicated = %d, want 2", obs.stats().Deduplicated)
	}
}

func TestDedupPermitsStrongerDispositionAndSuppressesRepeat(t *testing.T) {
	var mu sync.Mutex
	var got []Finding
	obs := NewObserver(Config{
		Enabled:       true,
		QueueCapacity: 8,
		Sink: func(f Finding) {
			mu.Lock()
			got = append(got, f)
			mu.Unlock()
		},
	})

	// 1. Early incomplete (unsettled program lag) for RuleMissingChildTerminal.
	incomplete := cleanSettledObservation()
	incomplete.ProgramID = "prog-upgrade"
	incomplete.ProgramSettled = false
	incomplete.ProgramState = "running"

	// 2. Later proven contradiction for the same (RuleMissingChildTerminal, "prog-upgrade").
	contradiction := cleanSettledObservation()
	contradiction.ProgramID = "prog-upgrade"
	contradiction.Executions = append(contradiction.Executions, ExecutionFact{
		ExecutionID: "exec-missing", OperationID: "grep", State: "running", Admitted: true, OwnerStarted: true,
	})

	submitObservation(obs, incomplete)
	submitObservation(obs, contradiction)
	submitObservation(obs, contradiction)
	obs.Close()

	mu.Lock()
	defer mu.Unlock()
	if len(got) != 2 {
		t.Fatalf("expected 2 findings (incomplete then contradiction), got %d: %+v", len(got), got)
	}
	if got[0].RuleID != RuleMissingChildTerminal || got[0].Disposition != DispositionIncomplete {
		t.Fatalf("first finding = %+v, want incomplete", got[0])
	}
	if got[1].RuleID != RuleMissingChildTerminal || got[1].Disposition != DispositionContradiction {
		t.Fatalf("second finding = %+v, want contradiction", got[1])
	}
	if obs.stats().Deduplicated != 1 {
		t.Fatalf("stats.Deduplicated = %d, want 1", obs.stats().Deduplicated)
	}
}

func TestRedactionScrubsSecretsAndDigestsPaths(t *testing.T) {
	const bearer = "Bearer sk-ant-api03-verysecretvalue1234567890"
	const apiKey = "OPENAI_API_KEY=sk-1234567890abcdef1234567890"
	const pathCanary = "/Users/dev/secret_workspace_canary/internal/private_file.go"

	digest := DigestReference("/Users/dev/secret_workspace_canary", "internal/private_file.go")
	if !strings.HasPrefix(digest, "ref:") || len(digest) != 16 || strings.Contains(digest, "secret") {
		t.Fatalf("unexpected digest format: %q", digest)
	}

	outFile := filepath.Join(t.TempDir(), "command-diagnostics.jsonl")
	t.Setenv("LYCAON_DEV_COMMAND_DIAGNOSTICS", "1")
	t.Setenv("LYCAON_DEV_COMMAND_DIAGNOSTICS_FILE", outFile)
	Close()
	t.Cleanup(Close)

	obs := cleanSettledObservation()
	obs.LinkedOperations[0].SourceID = "root:file?" + bearer + "&" + apiKey
	obs.LinkedOperations[0].ExpectedSourceID = pathCanary
	if !submitObservation(DefaultObserver(), obs) {
		t.Fatal("expected observer to accept observation when enabled")
	}
	Close()

	raw, err := os.ReadFile(outFile)
	testutil.FailErr(t, "read diagnostics jsonl", err)
	body := string(raw)
	for _, forbidden := range []string{"sk-ant-api03", "sk-1234567890abcdef", "secret_workspace_canary", "private_file.go"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("diagnostics output leaked %q: %s", forbidden, body)
		}
	}
	var decoded Finding
	lines := strings.Split(strings.TrimSpace(body), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected 1 finding line, got %d: %s", len(lines), body)
	}
	testutil.FailErr(t, "decode finding jsonl", json.Unmarshal([]byte(lines[0]), &decoded))
	if decoded.RuleID != RuleOperationSourceContradiction || decoded.Disposition != DispositionContradiction {
		t.Fatalf("unexpected decoded finding: %+v", decoded)
	}
}

type observerStats struct {
	Enqueued     int64
	Dropped      int64
	Emitted      int64
	Deduplicated int64
}

func (o *Observer) stats() observerStats {
	return observerStats{
		Enqueued:     o.enqueued.Load(),
		Dropped:      o.dropped.Load(),
		Emitted:      o.emitted.Load(),
		Deduplicated: o.deduplicated.Load(),
	}
}

func submitObservation(observer *Observer, obs Observation) bool {
	return observer.SubmitTask(Task{
		Fetch: func(context.Context) (Observation, error) { return obs, nil },
	})
}
