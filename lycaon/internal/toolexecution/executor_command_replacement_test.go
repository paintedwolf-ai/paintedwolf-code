package toolexecution

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolprofiles"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/toolsurface"
)

func TestExactReplacementRequiresCallableNativeHandler(t *testing.T) {
	executor := NewExecutor(nil, nil, "")
	if reject := executor.exactCommandReplacementReject(
		context.Background(), "command", "implement", map[string]any{"command": "sleep 2"}, tools.ToolContext{},
	); reject != nil {
		t.Fatalf("unregistered replacement blocked command: %+v", reject)
	}
	tc := tools.ToolContext{}
	if reject := commandFileWriteReject(
		context.Background(), executor, "command", "implement", map[string]any{"command": "cat <<'EOF' > index.html\n<h1>Hello</h1>\nEOF"}, &tc,
	); reject != nil {
		t.Fatalf("unregistered write blocked command: %+v", reject)
	}
}

func TestCommandFileWriteRejectRedirectsToWriteOrRequestTools(t *testing.T) {
	wireFixtureGuidanceRenderer(t)
	boundary := sandbox.NewBoundary(fixtureSandboxConfig(), fixtureToolProfiles(t))
	registry := tools.NewDefaultRegistry()
	noop := func(context.Context, map[string]any, tools.ToolContext) (string, error) { return `{"ok":true}`, nil }
	testutil.FailErr(t, "register command", registry.Register("command", noop))
	testutil.FailErr(t, "register write", registry.Register("write", noop))
	testutil.FailErr(t, "register request_tools", registry.Register("request_tools", noop))
	executor := NewExecutor(toolprofiles.NewProfilePolicyEngine(boundary), registry, "implement")
	root := t.TempDir()
	heredocArgs := map[string]any{"command": "cat <<'EOF' > index.html\n<h1>Hello</h1>\nEOF"}

	// Non-write command returns nil.
	baseTC := fixtureToolContext(root)
	if rej := commandFileWriteReject(context.Background(), executor, "command", "implement", map[string]any{"command": "git status"}, &baseTC); rej != nil {
		t.Fatalf("unexpected reject for non-write command: %+v", rej)
	}

	// Empty TurnOfferedToolNames populates command+write and emits write replacement call.
	_, err := executor.Invoke(context.Background(), "command", heredocArgs, fixtureToolContext(root))
	refusal, ok := guidance.RefusalFromError(err)
	if err == nil || !ok || refusal.Code() != "USE_WRITE_NATIVE" {
		t.Fatalf("Invoke heredoc error = %v, want USE_WRITE_NATIVE", err)
	}

	// Explicit TurnOfferedToolNames containing write and request_tools prefers direct write.
	offeredWriteTC := fixtureToolContext(root)
	offeredWriteTC.Turn.TurnOfferedToolNames = []string{"command", "write", "request_tools"}
	rej := commandFileWriteReject(context.Background(), executor, "command", "implement", heredocArgs, &offeredWriteTC)
	if rej == nil || rej.Code != "USE_WRITE_NATIVE" || rej.Data["tool_loadable"] != false {
		t.Fatalf("expected direct write reject, got %+v", rej)
	}

	// Explicit TurnOfferedToolNames with request_tools (and write deferred) emits request_tools(need: "write").
	deferredWriteTC := fixtureToolContext(root)
	deferredWriteTC.Turn.TurnOfferedToolNames = []string{"command", "request_tools"}
	deferredWriteTC.Turn.TurnToolPlan = toolsurface.Compile([]string{"command", "request_tools"}, []string{"write"})
	rejDeferred := commandFileWriteReject(context.Background(), executor, "command", "implement", heredocArgs, &deferredWriteTC)
	if rejDeferred == nil || rejDeferred.Code != "USE_WRITE_NATIVE" || rejDeferred.Data["tool_loadable"] != true {
		t.Fatalf("expected loadable write reject, got %+v", rejDeferred)
	}
}
