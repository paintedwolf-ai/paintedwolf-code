package worker

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/observability/cmddiag"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestObserveCommittedPromotionWEAParityAndBounds(t *testing.T) {
	in := promoteFinishInput{
		sessionID: "11111111-2222-3333-4444-555555555555",
		jobID:     "job-prom-1",
		task: &api.WorkerTask{
			ID:              "job-prom-1",
			ParentSessionID: "11111111-2222-3333-4444-555555555555",
			ProjectID:       "proj-1",
			WorkerName:      "worker-alpha",
			ResultHandle:    "res-prom-1",
			TouchedPaths:    []string{"internal/a.go", "internal/b.go"},
		},
		landed: []string{"internal/a.go", "internal/b.go"},
	}

	// 1. Disabled: zero allocations, returns false.
	restoreOff := cmddiag.SetObserver(cmddiag.NewObserver(cmddiag.Config{Enabled: false}))
	if observeCommittedPromotionWEA(in) {
		restoreOff()
		t.Fatal("expected false when diagnostics disabled")
	}
	allocs := testing.AllocsPerRun(100, func() {
		_ = observeCommittedPromotionWEA(in)
	})
	restoreOff()
	if allocs != 0 {
		t.Fatalf("disabled observeCommittedPromotionWEA allocated %v allocs/op, want 0", allocs)
	}

	// 2. Enabled: collect findings across clean, missing-proof, truncated, and contradiction cases.
	var mu sync.Mutex
	var findings []cmddiag.Finding
	obs := cmddiag.NewObserver(cmddiag.Config{
		Enabled:       true,
		QueueCapacity: 16,
		Now:           func() time.Time { return time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC) },
		Sink: func(f cmddiag.Finding) {
			mu.Lock()
			findings = append(findings, f)
			mu.Unlock()
		},
	})
	restoreOn := cmddiag.SetObserver(obs)
	defer restoreOn()

	// 2a. Clean promotion within bounds -> 0 findings.
	if !observeCommittedPromotionWEA(in) {
		t.Fatal("expected clean promotion to enqueue")
	}

	// 2b. Missing settled changed-path proof (must NOT fall back to in.landed) -> incomplete.
	noProof := in
	noProofTask := *in.task
	noProofTask.ResultHandle = "res-prom-no-proof"
	noProofTask.TouchedPaths = nil
	noProofTask.Files = nil
	noProof.task = &noProofTask
	if !observeCommittedPromotionWEA(noProof) {
		t.Fatal("expected missing-proof promotion to enqueue")
	}

	// 2c. Truncated changed-path proof (> weaMaxDiagPaths) with landed path beyond cap -> incomplete (not contradiction).
	trunc := in
	truncTask := *in.task
	truncTask.ResultHandle = "res-prom-trunc"
	truncTask.TouchedPaths = make([]string, 150)
	for i := 0; i < 150; i++ {
		truncTask.TouchedPaths[i] = fmt.Sprintf("pkg/file_%03d.go", i)
	}
	trunc.task = &truncTask
	trunc.landed = []string{"pkg/file_149.go"}
	if !observeCommittedPromotionWEA(trunc) {
		t.Fatal("expected truncated promotion to enqueue")
	}

	// 2d. Seeded contradiction: landed path absent from complete settled TouchedPaths -> contradiction.
	bad := in
	badTask := *in.task
	badTask.ResultHandle = "res-prom-bad"
	bad.task = &badTask
	bad.landed = []string{"internal/a.go", "internal/unlisted_secret.go"}
	if !observeCommittedPromotionWEA(bad) {
		t.Fatal("expected contradiction promotion to enqueue")
	}
	obs.Close()

	mu.Lock()
	defer mu.Unlock()
	byHandle := map[string]cmddiag.Finding{}
	for _, f := range findings {
		byHandle[f.ProgramID] = f
	}
	if _, ok := byHandle["res-prom-1"]; ok {
		t.Fatalf("clean promotion emitted unexpected finding: %+v", byHandle["res-prom-1"])
	}
	if f, ok := byHandle["res-prom-no-proof"]; !ok || f.RuleID != cmddiag.RuleWEALandedPathMismatch || f.Disposition != cmddiag.DispositionIncomplete {
		t.Fatalf("missing proof must emit incomplete, got %+v (all=%+v)", f, findings)
	}
	if f, ok := byHandle["res-prom-trunc"]; !ok || f.RuleID != cmddiag.RuleWEALandedPathMismatch || f.Disposition != cmddiag.DispositionIncomplete {
		t.Fatalf("truncated proof must emit incomplete (never false contradiction), got %+v (all=%+v)", f, findings)
	}
	if f, ok := byHandle["res-prom-bad"]; !ok || f.RuleID != cmddiag.RuleWEALandedPathMismatch || f.Disposition != cmddiag.DispositionContradiction {
		t.Fatalf("unlisted landed path must emit contradiction, got %+v (all=%+v)", f, findings)
	}
}
