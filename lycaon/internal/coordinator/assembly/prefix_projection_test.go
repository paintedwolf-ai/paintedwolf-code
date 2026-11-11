package assembly

import (
	"context"
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestKnownPrefixMatchesCompletionBeforeDynamicContext(t *testing.T) {
	engine := prefixStabilityEngine(t)
	sess := &api.Session{ID: "prepared", Posture: api.SessionPostureBuild, AgentType: orchestration.ProfileCoordinator, WorkspacePath: t.TempDir()}
	history := []api.Message{{Role: api.MessageRoleUser, Content: "Known request"}, {Role: api.MessageRoleAssistant, Content: "Known answer"}}
	frame, err := engine.deps().CoordinatorFrame.BuildCoordinatorTurnFrame(t.Context(), sess.ID, sess)
	testutil.FailErr(t, "read fixture frame", err)
	known, err := engine.BuildKnownPrefix(t.Context(), sess, history, frame)
	testutil.FailErr(t, "project known prefix", err)
	engine.BeginPromptTurn(sess.ID)
	full, err := engine.BuildCompletionMessages(t.Context(), sess, history, &frame)
	testutil.FailErr(t, "assemble completion", err)
	if len(known) < 3 || len(full) < len(known) || !reflect.DeepEqual(known, full[:len(known)]) {
		t.Fatalf("known projection differs from completion prefix: known=%d full=%d", len(known), len(full))
	}
	if known[0].PromptCacheBreakpoint != api.PromptCacheTierStanding || known[len(known)-1].PromptCacheBreakpoint != api.PromptCacheTierHistory {
		t.Fatal("projection lost standing or history boundary")
	}
}

type forbiddenPreparationGuidance struct{ t *testing.T }

func (g forbiddenPreparationGuidance) PrependGuidance(context.Context, string, []api.Message) []api.Message {
	g.t.Fatal("preparation consumed pending scan guidance")
	return nil
}

func TestKnownPrefixDoesNotAdvanceTurnOrWorkflow(t *testing.T) {
	engine := prefixStabilityEngine(t)
	sess := &api.Session{ID: "read-only", Posture: api.SessionPostureBuild, AgentType: orchestration.ProfileCoordinator, WorkspacePath: t.TempDir()}
	source := &advancingTurnFrameSource{phase: "boot", revision: 1}
	deps := engine.deps()
	deps.CoordinatorFrame = source
	deps.Board = advancingBoard{}
	deps.BoardOrientReady = advancingOrientRecorder{source: source}
	deps.ScanGuidance = forbiddenPreparationGuidance{t}
	deps.AgentsMDPolicies = func(context.Context, *api.Session, []api.Message) ([]api.Message, error) {
		t.Fatal("preparation resolved turn policy delivery")
		return nil, nil
	}
	engine.SetDeps(deps)
	engine.BeginPromptTurn(sess.ID, "pending")
	turn := engine.cache.LoadTurn(sess.ID)
	turn.Iteration = 7
	before := *turn
	_, err := engine.BuildKnownPrefix(t.Context(), sess, nil, inject.CoordinatorTurnFrame{})
	testutil.FailErr(t, "project without turn effects", err)
	if !reflect.DeepEqual(before, *turn) || source.phase != "boot" || source.revision != 1 {
		t.Fatal("preparation advanced turn or workflow state")
	}
}
