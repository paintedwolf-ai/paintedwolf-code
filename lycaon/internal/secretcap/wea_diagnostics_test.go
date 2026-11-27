package secretcap

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/observability/cmddiag"
)

func TestReferenceEchoesWEADiagnosticsParityAndLeakDetection(t *testing.T) {
	const canary = "pw_prod_seam_canary_0123456789abcdef"
	res := &Resolution{
		values: map[string]resolvedValue{
			"sec_canary_1": {value: canary},
		},
	}
	input := "header: Bearer " + canary + " done"

	// 1. Disabled run: exact output + 0 allocs on observer hook.
	restoreOff := cmddiag.SetObserver(cmddiag.NewObserver(cmddiag.Config{Enabled: false}))
	outOff := res.ReferenceEchoes(input)
	allocs := testing.AllocsPerRun(100, func() {
		_ = res.observeScreenedEchoWEA(outOff)
	})
	restoreOff()
	if allocs != 0 {
		t.Fatalf("disabled observeScreenedEchoWEA allocated %v allocs/op, want 0", allocs)
	}

	// 2. Enabled run: identical output, 0 findings on healthy screened output.
	var mu sync.Mutex
	var findings []cmddiag.Finding
	obs := cmddiag.NewObserver(cmddiag.Config{
		Enabled:       true,
		QueueCapacity: 8,
		Now:           func() time.Time { return time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC) },
		Sink: func(f cmddiag.Finding) {
			mu.Lock()
			findings = append(findings, f)
			mu.Unlock()
		},
	})
	restoreOn := cmddiag.SetObserver(obs)
	defer restoreOn()

	outOn := res.ReferenceEchoes(input)
	if outOff != outOn {
		t.Fatalf("ReferenceEchoes enabled/disabled mismatch: off=%q on=%q", outOff, outOn)
	}
	if strings.Contains(outOn, canary) {
		t.Fatalf("ReferenceEchoes did not screen canary: %q", outOn)
	}

	// 3. Seeded leak into post-screen check: fires RuleWEASecretCanaryLeak with ZERO raw canary bytes.
	if !res.observeScreenedEchoWEA("unscreened leak: " + canary) {
		t.Fatal("expected seeded leak observation to enqueue")
	}
	obs.Close()

	mu.Lock()
	defer mu.Unlock()
	if len(findings) != 1 {
		t.Fatalf("expected 1 finding (for seeded leak only), got %+v", findings)
	}
	if findings[0].RuleID != cmddiag.RuleWEASecretCanaryLeak || findings[0].Disposition != cmddiag.DispositionContradiction {
		t.Fatalf("unexpected finding: %+v", findings[0])
	}
	raw, err := json.Marshal(findings[0])
	if err != nil {
		t.Fatalf("marshal finding: %v", err)
	}
	if strings.Contains(string(raw), canary) {
		t.Fatalf("emitted finding leaked raw canary bytes: %s", string(raw))
	}
}
