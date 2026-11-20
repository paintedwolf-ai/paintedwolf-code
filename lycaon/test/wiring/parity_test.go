package wiring

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolrejection"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// TestHarnessPostureToolFilterParity verifies production wiring exposes posture rules
// and filters plan_from_blueprint from spec coordinator tool lists.
func TestHarnessPostureToolFilterParity(t *testing.T) {
	h := BuildForTest(t, WithRecordingLLM())
	ctx := context.Background()
	policy := h.Sessions.Manager.Coordinator.Guards.Policy()
	offers := func(sess *wire.Session) (bool, int) {
		listed := policy.ListForPrompt(ctx, sess, orchestration.ProfileCoordinator)
		for _, meta := range listed {
			if meta.Name == "plan_from_blueprint" {
				return true, len(listed)
			}
		}
		return false, len(listed)
	}

	control, err := h.CreateHarnessSession(t, wire.CreateSessionRequest{Posture: wire.SessionPostureBuild}, t.TempDir())
	testutil.FailErr(t, "create build session", err)
	if listed, _ := offers(control); !listed {
		t.Fatal("build coordinator must list plan_from_blueprint")
	}
	testutil.FailErr(t, "build posture invoke", policy.EvaluateInvoke(ctx, control, "plan_from_blueprint", nil))

	sess, err := h.CreateHarnessSession(t, wire.CreateSessionRequest{Posture: wire.SessionPostureSpec}, t.TempDir())
	testutil.FailErr(t, "create spec session", err)
	listed, count := offers(sess)
	if count == 0 {
		t.Fatal("spec coordinator lists no tools")
	}
	if listed {
		t.Fatal("spec coordinator must omit plan_from_blueprint from listed tools")
	}
	reject := toolrejection.AsToolReject(policy.EvaluateInvoke(ctx, sess, "plan_from_blueprint", nil))
	if reject == nil || reject.Code != "SPEC_POSTURE_DELEGATION_FORBIDDEN" {
		t.Fatalf("spec posture invoke refusal = %+v, want SPEC_POSTURE_DELEGATION_FORBIDDEN", reject)
	}
}
