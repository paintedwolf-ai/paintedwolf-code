package progress

import (
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestTurnClockWirePreservesLiveSpanPrecision(t *testing.T) {
	root := t.Name()
	since := time.Date(2026, time.September, 10, 12, 0, 0, 987654321, time.UTC)
	clocks.mu.Lock()
	clocks.by[root] = &clockState{
		turn:    clockSpan{accruedMs: 1500, since: since},
		work:    clockSpan{accruedMs: 900, since: since},
		active:  1,
		opening: "msg-open",
	}
	clocks.mu.Unlock()
	t.Cleanup(func() { ForgetClock(root) })

	event := TurnClockWire(root, Clock(root))
	got, err := time.Parse(time.RFC3339Nano, event.RunningAt)
	testutil.FailErr(t, "parse running timestamp", err)
	if !got.Equal(since) {
		t.Fatalf("running timestamp = %s, want %s", got, since)
	}
	if event.ActiveMs != 1500 || event.WorkMs != 900 || !event.Running || event.OpeningMessageID != "msg-open" {
		t.Fatalf("running clock = %+v", event)
	}
	if event.SettledAt != "" {
		t.Fatalf("running clock before its first pause has settled_at %q", event.SettledAt)
	}
	TurnFinished(root)
	paused := TurnClockWire(root, Clock(root))
	if paused.Running || paused.RunningAt != "" {
		t.Fatalf("paused clock = %+v", paused)
	}
	if _, err := time.Parse(time.RFC3339Nano, paused.SettledAt); err != nil {
		t.Fatalf("paused clock settled_at = %q: %v", paused.SettledAt, err)
	}
}
