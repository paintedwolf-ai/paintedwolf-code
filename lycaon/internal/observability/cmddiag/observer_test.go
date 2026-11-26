package cmddiag

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
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
	if Observe(obs) {
		t.Fatal("expected Observe to return false when disabled")
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

	if !obs.Submit(bad) {
		t.Fatal("first submit should enqueue")
	}
	<-firstEntered

	// Fill the remaining buffer (cap=2) and push additional items to saturate it.
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

	stats := obs.Stats()
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

func TestRedactionScrubsSecretsAndClampsMetadata(t *testing.T) {
	const bearer = "Bearer sk-ant-api03-verysecretvalue1234567890"
	const apiKey = "OPENAI_API_KEY=sk-1234567890abcdef1234567890"

	outFile := filepath.Join(t.TempDir(), "command-diagnostics.jsonl")
	t.Setenv("LYCAON_DEV_COMMAND_DIAGNOSTICS", "1")
	t.Setenv("LYCAON_DEV_COMMAND_DIAGNOSTICS_FILE", outFile)
	Close()
	t.Cleanup(Close)

	obs := cleanSettledObservation()
	obs.LinkedOperations[0].SourceID = "root:file?" + bearer + "&" + apiKey
	obs.LinkedOperations[0].ExpectedSourceID = "root:other:" + strings.Repeat("x", 400)
	if !Observe(obs) {
		t.Fatal("expected Observe to accept observation when enabled")
	}
	Close()

	raw, err := os.ReadFile(outFile)
	testutil.FailErr(t, "read diagnostics jsonl", err)
	body := string(raw)
	if strings.Contains(body, "sk-ant-api03") || strings.Contains(body, "sk-1234567890abcdef") {
		t.Fatalf("diagnostics output leaked secret material: %s", body)
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
	for _, src := range decoded.SourceIDs {
		if len(src) > DefaultMaxFieldBytes+16 {
			t.Fatalf("source ID not bounded: len=%d", len(src))
		}
	}
}
