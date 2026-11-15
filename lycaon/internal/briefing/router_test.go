package briefing

import (
	"context"
	"testing"
)

func TestRouterDeliversByProfileAndToAll(t *testing.T) {
	router := NewRouter()
	var mine, all []string
	router.Handle("approval", func(_ context.Context, _ string, s Snapshot) { mine = append(mine, s.TargetKey) })
	router.HandleAll(func(_ context.Context, _ string, s Snapshot) { all = append(all, s.TargetKey) })
	router.PublishSnapshot(t.Context(), "p", Snapshot{ProfileID: "approval", TargetKey: "a"})
	router.PublishSnapshot(t.Context(), "p", Snapshot{ProfileID: "activity", TargetKey: "b"})
	if len(mine) != 1 || mine[0] != "a" || len(all) != 2 {
		t.Fatalf("mine %v all %v", mine, all)
	}
}
