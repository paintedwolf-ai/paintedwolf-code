package progress

import (
	"context"
	"testing"
)

func TestWriteObserverReleaseStopsDelivery(t *testing.T) {
	var kept, released []string
	releaseKept := RegisterWriteObserver(func(_ context.Context, evt WriteEvent) { kept = append(kept, evt.SessionID) })
	t.Cleanup(releaseKept)
	release := RegisterWriteObserver(func(_ context.Context, evt WriteEvent) { released = append(released, evt.SessionID) })

	NotifyWriteObservers(t.Context(), WriteEvent{SessionID: "before"})
	release()
	release()
	NotifyWriteObservers(t.Context(), WriteEvent{SessionID: "after"})

	if len(released) != 1 || released[0] != "before" {
		t.Fatalf("released observer saw %v, want only the event before release", released)
	}
	if len(kept) != 2 {
		t.Fatalf("remaining observer saw %v, want both events", kept)
	}
	RegisterWriteObserver(nil)()
}
