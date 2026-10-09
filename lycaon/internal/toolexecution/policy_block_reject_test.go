package toolexecution

import (
	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/toolprofiles"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/platform"
	"github.com/lycaon/lycaon/internal/sandbox"
)

// A profile denial leaves Invoke as a typed reject, so the ledger records
// policy_rejection. An untyped error settles as TOOL_OWNER_FAILED/owner_error,
// which no repeat counter charges and which ends a worker child.
func TestProfileDenialLeavesInvokeAsTypedReject(t *testing.T) {
	boundary := fixtureBoundary(t)
	reg := tools.NewDefaultRegistry()
	ran := func(context.Context, map[string]any, tools.ToolContext) (string, error) { return "ran", nil }
	if err := reg.Register("write", ran); err != nil {
		t.Fatalf("register write: %v", err)
	}
	exec := NewExecutor(toolprofiles.NewGuidanceRejectPolicy(toolprofiles.NewProfilePolicyEngine(boundary)), reg, "implement")

	tctx := fixtureToolContext(t.TempDir())
	tctx.Identity.Agent = "no-such-profile"
	_, err := exec.Invoke(context.Background(), "write", map[string]any{
		"path": "main.go", "content": "x",
	}, tctx)
	if err == nil {
		t.Fatal("a denied profile ran the tool")
	}
	refusal, rendered := guidance.RefusalFromError(err)
	if !rendered {
		t.Fatalf("profile denial did not leave the executor as a rendered refusal: %#v", err)
	}
	if refusal.Code() == "" {
		t.Fatalf("rendered refusal carries no Code: %q", refusal.Body)
	}
	reject := toolrejection.AsToolReject(err)
	if reject == nil {
		t.Fatal("rendered refusal lost the reject that caused it")
	}
	if reject.FailureClass != "policy_rejection" {
		t.Fatalf("failure class = %q, want policy_rejection", reject.FailureClass)
	}
}

// A runtime exclusion states its own code.
func TestRuntimeToolDenyStatesItsCode(t *testing.T) {
	boundary := fixtureBoundary(t)
	profile := toolprofiles.NewProfilePolicyEngine(boundary)
	profile.SetRuntimeToolDeny(func(name string) bool { return name == "web_search" })

	decision, err := profile.Evaluate(context.Background(), platform.PolicyContext{
		ToolName: "web_search", ProfileID: "implement",
	})
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if !decision.Blocked {
		t.Fatal("runtime-denied tool was not blocked")
	}
	if decision.RejectCode != "WEB_SEARCH_DISABLED" {
		t.Fatalf("reject code = %q, want WEB_SEARCH_DISABLED", decision.RejectCode)
	}
	if decision.BlockReason != "WEB_SEARCH_DISABLED" {
		t.Fatalf("block reason = %q, want WEB_SEARCH_DISABLED", decision.BlockReason)
	}
}

// The executor's last exit renders a reject that reached it still typed.
func TestUnrenderedRejectIsRenderedAtTheBoundary(t *testing.T) {
	exec := NewExecutor(nil, tools.NewDefaultRegistry(), "implement")
	bare := &toolrejection.ToolReject{Code: "SANDBOX_SOCKS_PROXY_INVALID", Data: map[string]any{
		"reason": "socks_proxy must be a boolean",
	}}

	out := exec.Rejections.renderUnrenderedReject("command", bare)
	refusal, rendered := guidance.RefusalFromError(out)
	if !rendered {
		t.Fatalf("bare reject left the boundary unrendered: %#v", out)
	}
	if refusal.Code() != bare.Code {
		t.Fatalf("rendered code = %q, want %q", refusal.Code(), bare.Code)
	}
	if !strings.Contains(refusal.Body, "socks_proxy must be a boolean") {
		t.Fatalf("rendered body dropped the cause: %q", refusal.Body)
	}
	if toolrejection.AsToolReject(out) == nil {
		t.Fatal("rendering lost the reject that caused it")
	}
}

// The backstop renders refusals; it does not invent them.
func TestBoundaryLeavesPlainErrorsAlone(t *testing.T) {
	exec := NewExecutor(nil, tools.NewDefaultRegistry(), "implement")
	if got := exec.Rejections.renderUnrenderedReject("command", nil); got != nil {
		t.Fatalf("nil error became %#v", got)
	}
	plain := context.Canceled
	if got := exec.Rejections.renderUnrenderedReject("command", plain); !errors.Is(got, plain) {
		t.Fatalf("plain error rewritten to %#v", got)
	}
}

func TestExecutorPreservesHandlerCancellationAndPartialOutput(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	reg := tools.NewDefaultRegistry()
	if err := reg.Register("read", func(ctx context.Context, _ map[string]any, _ tools.ToolContext) (string, error) {
		cancel()
		return "partial output", ctx.Err()
	}); err != nil {
		t.Fatalf("register read: %v", err)
	}
	executor := NewExecutor(nil, reg, "implement")
	output, err := executor.Invoke(ctx, "read", map[string]any{"path": "fixture.txt"}, fixtureToolContext(t.TempDir()))
	if !errors.Is(err, context.Canceled) || toolrejection.AsToolReject(err) != nil {
		t.Fatalf("cancellation became owner rejection: %v", err)
	}
	if output != "partial output" {
		t.Fatalf("partial output = %q", output)
	}
}

func TestExecutorPreservesStructuredFailurePartialOutput(t *testing.T) {
	reg := tools.NewDefaultRegistry()
	const partial = `{"changed_paths":["retained.txt"],"completed":false}`
	cause := &toolrejection.ToolReject{Code: "GIT_OPERATION_FAILED", Data: map[string]any{"attempted": true}}
	if err := reg.Register("read", func(context.Context, map[string]any, tools.ToolContext) (string, error) { return partial, cause }); err != nil {
		t.Fatalf("register fixture: %v", err)
	}
	executor := NewExecutor(nil, reg, "implement")
	output, err := executor.Invoke(t.Context(), "read", map[string]any{"path": "fixture.txt"}, fixtureToolContext(t.TempDir()))
	reject := toolrejection.AsToolReject(err)
	if output != partial || reject == nil || reject.Code != cause.Code {
		t.Fatalf("partial outcome or failure identity lost: %q %v", output, err)
	}
}

func TestCoordinatorProfileDenialDoesNotInventDispatchState(t *testing.T) {
	boundary := sandbox.NewBoundary(sandbox.Config{}, []sandbox.ToolProfile{{ID: "coordinator", Tools: map[string]bool{"read": true}}})
	policy := toolprofiles.NewGuidanceRejectPolicy(toolprofiles.NewProfilePolicyEngine(boundary))
	for _, name := range []string{"state_update", "write"} {
		decision, err := policy.Evaluate(t.Context(), platform.PolicyContext{ToolName: name, ProfileID: "coordinator"})
		if err != nil {
			t.Fatalf("evaluate %s: %v", name, err)
		}
		if !decision.Blocked || decision.RejectCode != "COORDINATOR_TOOL_DENIED" {
			t.Fatalf("profile denial invented workflow state: %+v", decision)
		}
	}
}
