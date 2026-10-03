package assembly

import (
	"testing"

	"github.com/lycaon/lycaon/internal/settings"
)

func TestSessionPromptCacheBeginEndTurn(t *testing.T) {
	var cache SessionPromptCache
	cache.BeginTurn("sess-1", "kick-a")
	turn := cache.LoadTurn("sess-1")
	if len(turn.PendingKickIDs) != 1 || turn.PendingKickIDs[0] != "kick-a" {
		t.Fatalf("PendingKickIDs = %v", turn.PendingKickIDs)
	}
	turn.Iteration = 7
	cache.EndTurn("sess-1")
	if next := cache.LoadTurn("sess-1"); next == turn || next.Iteration != 0 || len(next.PendingKickIDs) != 0 {
		t.Fatalf("expected fresh turn after end, got %+v", next)
	}
}

func TestSessionPromptCacheEnabledAlwaysOn(t *testing.T) {
	if settings.DefaultSessionLimits().SettingsFingerprint() == "" {
		t.Fatal("expected non-empty settings fingerprint")
	}
}
