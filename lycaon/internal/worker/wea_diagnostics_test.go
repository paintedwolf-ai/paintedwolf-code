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

	// Disabled: zero allocations, returns false.
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

	// Enabled clean promotion (including > weaMaxDiagPaths bounded collection): zero findings.
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

	large := in
	largeTask := *in.task
	largeTask.TouchedPaths = make([]string, 150)
	large.landed = make([]string, 150)
	for i := 0; i < 150; i++ {
		p := fmt.Sprintf("pkg/file_%03d.go", i)
		largeTask.TouchedPaths[i] = p
		large.landed[i] = p
	}
	large.task = &largeTask
	if !observeCommittedPromotionWEA(large) {
		t.Fatal("expected clean promotion to enqueue")
	}

	// Seeded contradiction: landed path absent from committed worker TouchedPaths.
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
	if len(findings) != 1 {
		t.Fatalf("expected exactly 1 finding (for unlisted landed path), got %+v", findings)
	}
	if findings[0].RuleID != cmddiag.RuleWEALandedPathMismatch || findings[0].Disposition != cmddiag.DispositionContradiction {
		t.Fatalf("unexpected finding: %+v", findings[0])
	}
}
