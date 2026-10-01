package assembly

import (
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
)

func TestTransitionInjectKeyStableAcrossRetries(t *testing.T) {
	transition := surface.TransitionVars{ExecutionModeEntered: surface.ExecutionModeFamilyOrchestrate}
	k1 := inject.TransitionInjectKey("sess-1", 3, transition)
	k2 := inject.TransitionInjectKey("sess-1", 3, transition)
	if k1 != k2 || k1 == "" {
		t.Fatalf("transition inject key unstable: %q vs %q", k1, k2)
	}
	for _, changed := range []string{
		inject.TransitionInjectKey("sess-2", 3, transition),
		inject.TransitionInjectKey("sess-1", 4, transition),
		inject.TransitionInjectKey("sess-1", 3, surface.TransitionVars{ExecutionModeLeft: surface.ExecutionModeFamilyOrchestrate}),
	} {
		if changed == k1 {
			t.Fatalf("changed transition reused dedup key %q", k1)
		}
	}
}

func TestTransitionInjectDedupScratch(t *testing.T) {
	cache := &SessionPromptCache{}
	cache.BeginTurn("sess-1", "")
	turn := cache.LoadTurn("sess-1")
	transition := surface.TransitionVars{ExecutionModeLeft: surface.ExecutionModeFamilyInvestigate}
	key := inject.TransitionInjectKey("sess-1", turn.PromptTurnSeq, transition)
	turn.TransitionInjectKey = key
	turn.TransitionInjectBlock = "once"
	if reloaded := cache.LoadTurn("sess-1"); reloaded.TransitionInjectBlock != "once" || reloaded.TransitionInjectKey != key {
		t.Fatalf("transition scratch was not retained: %+v", reloaded)
	}
	if other := cache.LoadTurn("sess-2"); other.TransitionInjectBlock != "" || other.TransitionInjectKey != "" {
		t.Fatalf("transition scratch leaked into another session: %+v", other)
	}
	cache.EndTurn("sess-1")
	cache.BeginTurn("sess-1", "")
	if next := cache.LoadTurn("sess-1"); next.TransitionInjectBlock != "" || next.TransitionInjectKey != "" {
		t.Fatalf("transition scratch survived into the next turn: %+v", next)
	}
}
