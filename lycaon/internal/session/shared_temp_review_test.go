package session

import (
	"context"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/authzledger"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/session/approvalstate"
	"github.com/lycaon/lycaon/internal/tools/native/command"
	"github.com/lycaon/lycaon/pkg/api"
)

type tempDecisionFixture struct {
	unusedWriteRootCheckpoints
	status  hitl.DecisionStatus
	runtime *approvalstate.SandboxPathGrantRuntime
	cards   []hitl.CheckpointRequest
}

func (c *tempDecisionFixture) RequestCheckpoint(_ context.Context, req hitl.CheckpointRequest) (*hitl.CheckpointResponse, error) {
	c.cards = append(c.cards, req)
	if c.status == hitl.DecisionStatusApproved {
		for _, opt := range req.ApprovalPlan.Options {
			if opt.Rung != hitl.ApprovalRungChat || opt.Disabled || opt.Group != "" {
				continue
			}
			for _, delta := range opt.Authority {
				if delta.Kind == hitl.AuthorityWriteRootChat {
					for _, path := range delta.WriteRoots {
						c.runtime.GrantChat(delta.ChatSession(), path, delta.Grant.ID, "checkpoint", nil)
					}
				}
			}
			break
		}
	}
	return &hitl.CheckpointResponse{CheckpointID: "checkpoint", Status: c.status, ResolvedBy: authzledger.ResolvedByHuman, Result: &hitl.DecisionResult{Approved: c.status == hitl.DecisionStatusApproved}}, nil
}

func TestSharedTempSettlementAndChatScope(t *testing.T) {
	t.Setenv("LYCAON_CONFIG_DIR", "/opt/shared-temp-review-fixture/config")
	roots := []string{confine.SharedTempLocation("/tmp"), confine.SharedTempLocation("/var/tmp")}
	for _, status := range []hitl.DecisionStatus{hitl.DecisionStatusApproved, hitl.DecisionStatusRejected, hitl.DecisionStatusExpired} {
		t.Run(string(status), func(t *testing.T) {
			runtime := approvalstate.NewSandboxPathGrantRuntime()
			cards := &tempDecisionFixture{runtime: runtime, status: status}
			broker := &WriteRootCheckpointBroker{Runtime: runtime, Checkpoints: cards, Posture: func(string) gate.Posture { return gate.PostureBalanced }}
			in := command.SandboxWriteRootAsk{SessionID: "chat", ToolName: "command", ToolCallID: "first", Command: "build"}
			result := broker.ReviewSharedTemp(t.Context(), in, roots, true)
			if len(cards.cards) != 1 || len(cards.cards[0].ApprovalPlan.Subject.Targets) != len(roots) {
				t.Fatalf("reviewed locations=%v result=%+v", cards.cards, result)
			}
			for i, target := range cards.cards[0].ApprovalPlan.Subject.Targets {
				if target.Kind != "shared_temp" || target.Label != roots[i] {
					t.Fatalf("target %d=%+v", i, target)
				}
			}
			var outcome []string
			switch status {
			case hitl.DecisionStatusApproved:
				outcome = result.Allowed
			case hitl.DecisionStatusRejected:
				outcome = result.Declined
			case hitl.DecisionStatusExpired:
				outcome = result.Unanswered
			}
			if !slices.Equal(outcome, roots) || len(result.Allowed)+len(result.Declined)+len(result.Unanswered) != len(roots) {
				t.Fatalf("status=%s outcome=%+v", status, result)
			}
			in.ToolCallID = "second"
			broker.ReviewSharedTemp(t.Context(), in, roots, true)
			expected := 1
			if status == hitl.DecisionStatusExpired {
				expected = 2
			}
			if len(cards.cards) != expected {
				t.Fatalf("second call raised %d cards", len(cards.cards)-1)
			}
			if other := broker.Runtime.SessionWriteRoots("other"); len(other) > 0 {
				t.Fatalf("foreign grant %v", other)
			}
		})
	}
}

func TestSharedTempToleratedOnceAndLightAuthorization(t *testing.T) {
	t.Setenv("LYCAON_CONFIG_DIR", "/opt/shared-temp-review-fixture/config")
	roots := []string{confine.SharedTempLocation("/tmp"), confine.SharedTempLocation("/var/tmp")}
	rt := approvalstate.NewSandboxPathGrantRuntime()
	cards := &tempDecisionFixture{runtime: rt}
	broker := &WriteRootCheckpointBroker{Runtime: rt, Checkpoints: cards, Posture: func(string) gate.Posture { return gate.PostureLight }}
	ask := command.SandboxWriteRootAsk{SessionID: "chat", ToolName: "verify"}
	for i := 0; i < 3; i++ {
		result := broker.ReviewSharedTemp(t.Context(), ask, roots, false)
		if (len(result.Refused) > 0) != (i == 0) || result.Tolerated != (i == 0) {
			t.Fatalf("run %d: %+v", i, result)
		}
	}
	ask.SessionID = "other"
	other := broker.ReviewSharedTemp(t.Context(), ask, roots, false)
	if !slices.Equal(other.Refused, roots) {
		t.Fatalf("other chat report=%+v", other)
	}
	result := broker.ReviewSharedTemp(t.Context(), ask, roots, true)
	if !slices.Equal(result.Allowed, roots) || len(cards.cards) != 0 {
		t.Fatalf("light review=%+v cards=%d", result, len(cards.cards))
	}
}

func TestSharedTempOccurrencePreservesOtherRecovery(t *testing.T) {
	mgr := newPostToolGuidanceManager(t)
	obs := confine.Observation{Applied: true, FailedStages: []string{"build"}, Refusals: confine.SandboxRefusals{Refusals: []confine.SandboxRefusal{
		{Operation: "file-write-create", Target: "/tmp/build", Layer: confine.FloorSharedTemp, Grant: "/tmp", Recovery: confine.RecoverWriteRoot},
		{Operation: "file-write-create", Target: "/outside/file", Layer: confine.FloorOutsideWriteRoots, Grant: "/outside", Recovery: confine.RecoverWriteRoot},
	}}}
	for _, tc := range []struct {
		settled guidance.SharedTempSettlement
		code    string
	}{
		{guidance.SharedTempSettlement{Refused: []string{"/tmp"}, Allowed: []string{"/tmp"}}, "SANDBOX_SHARED_TEMP_REFUSED"},
		{guidance.SharedTempSettlement{Refused: []string{"/tmp"}, Declined: []string{"/tmp"}, ScratchAvailable: true}, "SANDBOX_SHARED_TEMP_REFUSED"},
		{guidance.SharedTempSettlement{Refused: []string{"/tmp"}, Unanswered: []string{"/tmp"}}, "SANDBOX_SHARED_TEMP_REFUSED"},
		{guidance.SharedTempSettlement{Refused: []string{"/tmp"}, Tolerated: true}, "SANDBOX_SHARED_TEMP_TOLERATED"},
		{guidance.SharedTempSettlement{Refused: []string{"/tmp"}, Running: true}, "SANDBOX_SHARED_TEMP_REFUSED"},
	} {
		_, facts := mgr.ToolPolicy.AfterTool(t.Context(), &api.Session{ID: "chat"}, "command", map[string]any{"command": "build"}, "output", 1, guidance.ToolResultFacts{Confine: obs, SharedTemp: &tc.settled})
		if !facts.HasCode(tc.code) || !facts.HasCode("SANDBOX_TRY_WRITE_ROOT") || !facts.Succeeded() {
			t.Fatalf("settlement %+v -> %+v", tc.settled, facts)
		}
	}
}
