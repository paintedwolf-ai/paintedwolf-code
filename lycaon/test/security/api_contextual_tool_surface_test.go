package security

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolrejection"
	wire "github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/wiring"
)

// promptToolNames posts one prompt in posture and returns the tool names the
// coordinator request offered.
func promptToolNames(t *testing.T, posture wire.SessionPosture) map[string]bool {
	t.Helper()
	h := wiring.BuildForTest(t, wiring.WithRecordingLLM())
	sess := createSessionWithPostureHTTP(t, h.Server, t.TempDir(), posture)
	// plan_from_blueprint is progress-gated; seed progress so only posture decides.
	h.SeedProgress(t, context.Background(), sess.ID)
	postPromptAndWaitIdle(t, h.Server, sess.ID, "review plan scope")
	names := map[string]bool{}
	for _, tool := range h.Recording.LastRequest().Tools {
		names[tool.Name] = true
	}
	return names
}

func TestSpecCoordinatorPromptOmitsPlanFromBlueprint(t *testing.T) {
	spec := promptToolNames(t, wire.SessionPostureSpec)
	if len(spec) == 0 {
		t.Fatal("spec coordinator request offered no tools")
	}
	if spec["plan_from_blueprint"] {
		t.Fatalf("spec coordinator request offers plan_from_blueprint among %d tools", len(spec))
	}
	if build := promptToolNames(t, wire.SessionPostureBuild); !build["plan_from_blueprint"] {
		t.Fatalf("build coordinator request does not offer plan_from_blueprint among %d tools", len(build))
	}
}

func TestInvokeStillDeniesWhenToolNotListed(t *testing.T) {
	h := wiring.BuildForTest(t, wiring.WithRecordingLLM())
	sess := createSessionWithPostureHTTP(t, h.Server, t.TempDir(), wire.SessionPostureSpec)
	ctx := context.Background()
	sessionRec, err := h.Store.Get(ctx, sess.ID)
	testutil.FailErr(t, "store.Get failed", err)
	policy := h.Sessions.Manager.Coordinator.Guards.Policy()

	err = policy.EvaluateInvoke(ctx, sessionRec, "plan_from_blueprint", nil)
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject.Code != "SPEC_POSTURE_DELEGATION_FORBIDDEN" {
		t.Fatalf("plan_from_blueprint in spec posture: err = %v, want SPEC_POSTURE_DELEGATION_FORBIDDEN", err)
	}
	if err := policy.EvaluateInvoke(ctx, sessionRec, "read", map[string]any{"path": "README.md"}); err != nil {
		t.Fatalf("on-surface read in spec posture: %v", err)
	}
}
