package assembly

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestProgressPromptTracksChecklistWithinTurn(t *testing.T) {
	eng := prefixStabilityEngine(t)
	missing := true
	deps := eng.deps()
	deps.ImplementSessionState = func(context.Context, *api.Session) surface.ImplementSessionState {
		return surface.ImplementSessionState{WrapupGatesLoaded: true, ProgressMissing: missing}
	}
	eng.SetDeps(deps)
	sess := &api.Session{ID: "progress-state", Posture: api.SessionPostureBuild, AgentType: orchestration.ProfileCoordinator, WorkspacePath: t.TempDir()}
	eng.BeginPromptTurn(sess.ID, "")
	var previousKey string
	for i, state := range []bool{true, false, true} {
		missing = state
		eng.Cache().LoadTurn(sess.ID).Iteration = i
		if _, err := eng.BuildCompletionMessages(t.Context(), sess, nil, nil); err != nil {
			t.Fatalf("render progress state %v: %v", state, err)
		}
		turn := eng.Cache().LoadTurn(sess.ID)
		if got := strings.Contains(turn.StablePrompt, "No checklist is stored"); got != state {
			t.Fatalf("missing checklist guidance = %v, want %v", got, state)
		}
		if turn.StablePromptKey == previousKey {
			t.Fatal("checklist state changed without invalidating the prompt cache")
		}
		previousKey = turn.StablePromptKey
	}
}
