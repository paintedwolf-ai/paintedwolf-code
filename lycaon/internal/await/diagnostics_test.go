package await

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestPhaseObserverClockAndDispatchTime(t *testing.T) {
	noop := RegisterPhaseObserver(nil)
	noop()

	var mu sync.Mutex
	var records []PhaseRecord
	unsub := RegisterPhaseObserver(func(rec PhaseRecord) {
		mu.Lock()
		records = append(records, rec)
		mu.Unlock()
	})
	defer unsub()

	base := context.Background()
	if WithDispatchTime(base, time.Time{}) != base {
		t.Fatal("zero dispatch time wrapped context")
	}
	if !DispatchTimeFrom(base).IsZero() {
		t.Fatal("unexpected dispatch time on bare context")
	}
	fixed := time.Date(2026, 11, 23, 17, 0, 0, 0, time.UTC)
	ctx := WithDispatchTime(base, fixed)
	if !DispatchTimeFrom(ctx).Equal(fixed) {
		t.Fatalf("dispatch time = %v, want %v", DispatchTimeFrom(ctx), fixed)
	}

	var defaultClock Clock
	if defaultClock.Now().IsZero() {
		t.Fatal("default clock returned zero time")
	}
	ch, stop := defaultClock.Timer(time.Hour)
	if ch == nil {
		t.Fatal("default timer returned nil channel")
	}
	stop()

	var ticks int
	customCh := make(chan time.Time, 1)
	customClock := Clock{
		NowFunc: func() time.Time { return fixed },
		NewTimerFunc: func(time.Duration) (<-chan time.Time, func()) {
			ticks++
			return customCh, func() {}
		},
	}
	if !customClock.Now().Equal(fixed) {
		t.Fatal("custom clock Now mismatch")
	}
	gotCh, customStop := customClock.Timer(time.Second)
	customStop()
	if gotCh != customCh || ticks != 1 {
		t.Fatalf("custom timer ticks = %d", ticks)
	}

	EmitPhaseRecord(ctx, PhaseRecord{
		SessionID:   "s1",
		ToolCallID:  "call-1",
		Handle:      "h1",
		Tool:        "wait",
		Phase:       "wait_handler_return",
		Status:      "process_done",
		DurationMS:  4,
		ReferenceAt: fixed,
	})
	unsub()
	unsub()
	EmitPhaseRecord(ctx, PhaseRecord{Phase: "ignored"})

	mu.Lock()
	defer mu.Unlock()
	if len(records) != 1 || records[0].Handle != "h1" || records[0].RecordedAt.IsZero() {
		t.Fatalf("records = %+v", records)
	}
	raw, err := json.Marshal(records[0])
	if err != nil {
		t.Fatalf("marshal record: %v", err)
	}
	if strings.Contains(string(raw), "echo ") {
		t.Fatalf("record contained command body: %s", string(raw))
	}
}
