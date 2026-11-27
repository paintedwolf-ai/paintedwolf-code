package watch

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// ww02ManualClock implements Clock for deterministic virtual-time tests (D4 in-process lane).
type ww02ManualClock struct {
	mu  sync.Mutex
	now time.Time
}

var _ Clock = (*ww02ManualClock)(nil)

func newWW02ManualClock(start time.Time) *ww02ManualClock {
	return &ww02ManualClock{now: start.UTC()}
}

func (c *ww02ManualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *ww02ManualClock) Advance(d time.Duration) time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d).UTC()
	return c.now
}

func (c *ww02ManualClock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = t.UTC()
}

// ww02Barrier provides deterministic reach/release synchronization for injected in-process boundaries.
type ww02Barrier struct {
	reached  chan struct{}
	released chan struct{}
	reachOne sync.Once
	relOne   sync.Once
}

func newWW02Barrier() *ww02Barrier {
	return &ww02Barrier{
		reached:  make(chan struct{}),
		released: make(chan struct{}),
	}
}

func (b *ww02Barrier) Reach() {
	b.reachOne.Do(func() { close(b.reached) })
}

func (b *ww02Barrier) Release() {
	b.relOne.Do(func() { close(b.released) })
}

func (b *ww02Barrier) WaitReached(timeout time.Duration) bool {
	select {
	case <-b.reached:
		return true
	case <-time.After(timeout):
		return false
	}
}

func (b *ww02Barrier) WaitReleased(timeout time.Duration) bool {
	select {
	case <-b.released:
		return true
	case <-time.After(timeout):
		return false
	}
}

// ww02EvidenceBundle matches wait_watch/checker.py schema_version=1 for the in-process lane.
type ww02EvidenceBundle struct {
	SchemaVersion int            `json:"schema_version"`
	RunID         string         `json:"run_id"`
	Lane          string         `json:"lane"`
	ClockDomain   string         `json:"clock_domain"`
	CaseID        string         `json:"case_id"`
	Expectations  map[string]any `json:"expectations"`
	Observers     map[string]any `json:"observers"`
	SHA256Digests map[string]any `json:"sha256_digests"`
}

func writeWW02Evidence(t *testing.T, outPath string, bundle ww02EvidenceBundle) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		t.Fatalf("mkdir evidence dir: %v", err)
	}
	raw, err := json.MarshalIndent(bundle, "", "  ")
	if err != nil {
		t.Fatalf("marshal ww02 evidence: %v", err)
	}
	sum := sha256.Sum256(raw)
	if bundle.SHA256Digests == nil {
		bundle.SHA256Digests = map[string]any{}
	}
	bundle.SHA256Digests["bundle_payload"] = hex.EncodeToString(sum[:])
	raw, err = json.MarshalIndent(bundle, "", "  ")
	if err != nil {
		t.Fatalf("re-marshal ww02 evidence: %v", err)
	}
	if err := os.WriteFile(outPath, append(raw, '\n'), 0o644); err != nil {
		t.Fatalf("write ww02 evidence: %v", err)
	}
}

func TestWW02ManualClockAndDeadlineOrdering(t *testing.T) {
	base := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	clk := newWW02ManualClock(base)
	heap := NewDeadlineHeap()

	due := base.Add(30 * time.Second)
	// Insert in reverse priority order at the exact same instant.
	heap.Upsert(KindWaitDeadline, "wait-1", due)
	heap.Upsert(KindTimerTick, "tick-1", due)
	heap.Upsert(KindWatchExpiry, "watch-1", due)

	// Nothing due before advancing virtual clock.
	if popped := heap.PopDue(clk.Now()); len(popped) != 0 {
		t.Fatalf("PopDue before deadline = %v, want empty", popped)
	}

	// Verify barrier reach/release synchronization.
	b := newWW02Barrier()
	done := make(chan bool, 1)
	go func() {
		b.Reach()
		done <- b.WaitReleased(2 * time.Second)
	}()
	if !b.WaitReached(2 * time.Second) {
		t.Fatal("barrier did not reach")
	}
	b.Release()
	if !<-done {
		t.Fatal("barrier did not release")
	}

	// Advance manual clock to exact due instant: expiry must settle before timer tick before wait deadline.
	clk.Advance(30 * time.Second)
	popped := heap.PopDue(clk.Now())
	if len(popped) != 3 {
		t.Fatalf("PopDue len = %d, want 3", len(popped))
	}
	wantKinds := []DeadlineKind{KindWatchExpiry, KindTimerTick, KindWaitDeadline}
	for i, want := range wantKinds {
		if popped[i].Key.Kind != want {
			t.Fatalf("popped[%d].Kind = %v, want %v", i, popped[i].Key.Kind, want)
		}
	}

	// Verify evidence emission matches checker schema_version=1.
	outPath := filepath.Join(t.TempDir(), "ww02-clock-evidence.json")
	writeWW02Evidence(t, outPath, ww02EvidenceBundle{
		SchemaVersion: 1,
		RunID:         "inprocess-clock-test",
		Lane:          "in-process",
		ClockDomain:   "manual",
		CaseID:        "ww02-manual-clock-ordering",
		Expectations: map[string]any{
			"expected_outcome": "delivered",
			"owed_recipients":  []string{"session:s1"},
			"owed_occurrences": []string{"occ-clock-1"},
		},
		Observers: map[string]any{
			"source":         map[string]any{"observer": "source", "clock_domain": "manual"},
			"storage":        map[string]any{"observer": "storage", "clock_domain": "manual", "available": true, "armed_lease_count": 0},
			"admission":      map[string]any{"observer": "admission", "clock_domain": "manual"},
			"rendered_input": map[string]any{"observer": "rendered_input", "clock_domain": "manual", "presentations": []any{}},
			"den":            map[string]any{"observer": "den", "status": "not_run"},
		},
	})
	if info, err := os.Stat(outPath); err != nil || info.Size() == 0 {
		t.Fatalf("expected non-empty evidence file at %s: err=%v", outPath, err)
	}
}
