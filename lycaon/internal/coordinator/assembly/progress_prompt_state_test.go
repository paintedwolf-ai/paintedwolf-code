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
	var expectedKey string
	var expectedStable string
	for i, state := range []bool{true, false, true} {
		missing = state
		eng.Cache().LoadTurn(sess.ID).Iteration = i
		msgs, err := eng.BuildCompletionMessages(t.Context(), sess, nil, nil)
		if err != nil {
			t.Fatalf("render progress state %v: %v", state, err)
		}
		turn := eng.Cache().LoadTurn(sess.ID)
		if strings.Contains(turn.StablePrompt, "No checklist is stored") {
			t.Fatal("stable prompt must not include turn-level checklist guidance")
		}
		if i == 0 {
			expectedKey = turn.StablePromptKey
			expectedStable = turn.StablePrompt
		} else {
			if turn.StablePromptKey != expectedKey {
				t.Fatalf("stable prompt key changed across progress flips: got %q, want %q", turn.StablePromptKey, expectedKey)
			}
			if turn.StablePrompt != expectedStable {
				t.Fatal("stable prompt bytes changed across progress flips")
			}
		}
		tailContains := false
		for _, m := range msgs[1:] {
			if strings.Contains(m.Content, "No checklist is stored") {
				tailContains = true
				break
			}
		}
		if tailContains != state {
			t.Fatalf("tail checklist guidance present=%v, want %v", tailContains, state)
		}
	}
}
