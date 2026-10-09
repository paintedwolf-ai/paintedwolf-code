package findings

import (
	"context"
	"testing"
)

func TestAppendObserverReleaseStopsDelivery(t *testing.T) {
	var kept, released []string
	releaseKept := RegisterAppendObserver(func(_ context.Context, evt AppendEvent) { kept = append(kept, evt.SessionID) })
	t.Cleanup(releaseKept)
	release := RegisterAppendObserver(func(_ context.Context, evt AppendEvent) { released = append(released, evt.SessionID) })

	NotifyAppendObservers(t.Context(), AppendEvent{SessionID: "before"})
	release()
	release()
	NotifyAppendObservers(t.Context(), AppendEvent{SessionID: "after"})

	if len(released) != 1 || released[0] != "before" {
		t.Fatalf("released observer saw %v, want only the event before release", released)
	}
	if len(kept) != 2 {
		t.Fatalf("remaining observer saw %v, want both events", kept)
	}
	RegisterAppendObserver(nil)()
}
