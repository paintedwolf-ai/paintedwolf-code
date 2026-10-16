package toolexecution

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/turnload"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/llm/transcript"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcommand"
	"github.com/lycaon/lycaon/internal/toolprofiles"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/toolsurface"
	"github.com/lycaon/lycaon/pkg/api"
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

	// Missing offered context must still reject without inventing available tools.
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

func TestCommandFileWriteRecoveryUsesOnlyFrozenOfferedTools(t *testing.T) {
	wireFixtureGuidanceRenderer(t)
	boundary := sandbox.NewBoundary(fixtureSandboxConfig(), fixtureToolProfiles(t))
	registry := tools.NewDefaultRegistry()
	noop := func(context.Context, map[string]any, tools.ToolContext) (string, error) { return `{"ok":true}`, nil }
	for _, name := range []string{"command", "write", "mkdir", "request_tools"} {
		testutil.FailErr(t, "register recovery tool", registry.Register(name, noop))
	}
	executor := NewExecutor(toolprofiles.NewProfilePolicyEngine(boundary), registry, "implement")
	for _, tc := range []struct {
		name      string
		offered   []string
		deferred  []string
		command   string
		wantCalls bool
		loadable  bool
	}{
		{name: "absent snapshot"},
		{name: "empty snapshot", offered: []string{}},
		{name: "policy visibility is not loadability", offered: []string{"command", "request_tools"}},
		{name: "plan alone is not offered", deferred: []string{"write"}},
		{name: "offered write", offered: []string{"command", "write"}, wantCalls: true},
		{name: "deferred write", offered: []string{"command", "request_tools"}, deferred: []string{"write"}, wantCalls: true, loadable: true},
		{name: "unoffered mkdir", offered: []string{"command", "write"}, deferred: []string{"mkdir"}, command: "mkdir -p sub && cat > sub/out.txt"},
		{name: "offered mkdir", offered: []string{"command", "write", "mkdir"}, command: "mkdir -p sub && cat > sub/out.txt", wantCalls: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := fixtureToolContext(t.TempDir())
			ctx.Turn.TurnOfferedToolNames = tc.offered
			ctx.Turn.TurnToolPlan = toolsurface.Compile(tc.offered, tc.deferred)
			command := tc.command
			if command == "" {
				command = "cat > out.txt"
			}
			// Put the literal body on the writer, not on a multi-statement stdin stream.
			command += " <<'EOF'\nhello\nEOF"
			reject := commandFileWriteReject(t.Context(), executor, "command", "implement", map[string]any{"command": command}, &ctx)
			if reject == nil {
				t.Fatal("missing file-write rejection")
			}
			_, hasCalls := reject.Data["replacement_calls"]
			if hasCalls != tc.wantCalls || reject.Data["tool_loadable"] != tc.loadable {
				t.Fatalf("recovery=%#v, want calls=%t loadable=%t", reject.Data, tc.wantCalls, tc.loadable)
			}
			if !reflect.DeepEqual(ctx.Turn.TurnOfferedToolNames, tc.offered) {
				t.Fatalf("offered snapshot mutated: %#v", ctx.Turn.TurnOfferedToolNames)
			}
		})
	}
}

func TestExactReplacementUsesFrozenRecoverySurface(t *testing.T) {
	wireFixtureGuidanceRenderer(t)
	boundary := sandbox.NewBoundary(fixtureSandboxConfig(), fixtureToolProfiles(t))
	registry := tools.NewDefaultRegistry()
	noop := func(context.Context, map[string]any, tools.ToolContext) (string, error) { return "", nil }
	for _, name := range []string{"command", "wait", "request_tools"} {
		testutil.FailErr(t, "register recovery fixture", registry.Register(name, noop))
	}
	executor := NewExecutor(toolprofiles.NewProfilePolicyEngine(boundary), registry, "implement")
	for _, tc := range []struct {
		name              string
		offered, deferred []string
		want              string
		uncompiled        bool
	}{
		{name: "offered", offered: []string{"command", "wait"}, want: "wait"},
		{name: "deferred", offered: []string{"command", "request_tools"}, deferred: []string{"wait"}, want: "request_tools"},
		{name: "unavailable", offered: []string{"command"}},
		{name: "empty compiled context"},
		{name: "missing context", uncompiled: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := fixtureToolContext(t.TempDir())
			ctx.Turn.TurnOfferedToolNames = tc.offered
			ctx.Turn.TurnToolPlan = toolsurface.Compile(tc.offered, tc.deferred)
			if tc.uncompiled {
				ctx.Turn.TurnToolPlan = toolsurface.Plan{}
			}
			reject := executor.exactCommandReplacementReject(t.Context(), "command", "implement", map[string]any{"command": "sleep 2"}, ctx)
			if tc.want == "" {
				if reject != nil {
					t.Fatalf("unreachable replacement: %#v", reject.Data)
				}
				return
			}
			if reject == nil {
				t.Fatal("missing reachable recovery")
			}
			calls, _ := reject.Data["replacement_calls"].([]toolcommand.ReplacementCall)
			if len(calls) != 1 || calls[0].Tool != tc.want {
				t.Fatalf("recovery calls=%#v, want %s", calls, tc.want)
			}
			if tc.want == "request_tools" && calls[0].Args["need"] != "wait" {
				t.Fatal("known deferred target lost exact loader need")
			}
		})
	}
}

func TestNativeRecoverySequenceLoadsExactTargetsWithoutRanking(t *testing.T) {
	boundary := sandbox.NewBoundary(fixtureSandboxConfig(), fixtureToolProfiles(t))
	registry := tools.NewDefaultRegistry()
	noop := func(context.Context, map[string]any, tools.ToolContext) (string, error) { return "", nil }
	for _, name := range []string{"command", "write", "mkdir", "request_tools"} {
		testutil.FailErr(t, "register sequence target", registry.Register(name, noop))
	}
	activation := tools.NewMemoryActivation()
	testutil.FailErr(t, "register actual loader", tools.RegisterRequestTools(registry, tools.RequestToolsDeps{Activation: activation, Boundary: fakeRequestBoundary{}, Resolve: func(context.Context, tools.ToolContext, string, []turnload.ToolCard) turnload.RequestOutcome {
		t.Fatal("exact recovery invoked ranking")
		return turnload.RequestOutcome{}
	}}))
	executor := NewExecutor(toolprofiles.NewProfilePolicyEngine(boundary), registry, "implement")
	tc := fixtureToolContext(t.TempDir())
	tc.Identity.SessionID = "recovery"
	tc.Turn.TurnOfferedToolNames = []string{"command", "request_tools"}
	tc.Turn.TurnToolPlan = toolsurface.Compile(tc.Turn.TurnOfferedToolNames, []string{"mkdir", "write"})
	candidates := []toolcommand.ReplacementCall{{Tool: "mkdir", Args: map[string]any{"path": "sub"}}, {Tool: "write", Args: map[string]any{"path": "sub/out.txt", "content": "a"}}, {Tool: "write", Args: map[string]any{"path": "sub/out.txt", "content": "b", "append": true}}}
	recovery := executor.classifyNativeRecovery(t.Context(), "implement", tc, candidates, true)
	if len(recovery.calls) != 2 || !reflect.DeepEqual(recovery.loadTools, []string{"mkdir", "write"}) {
		t.Fatalf("loading recipe=%+v", recovery)
	}
	for i, call := range recovery.calls {
		if call.Tool != "request_tools" || call.Args["need"] != recovery.loadTools[i] {
			t.Fatalf("not one exact target: %+v", call)
		}
		_, err := registry.Run(t.Context(), call.Tool, call.Args, tc)
		testutil.FailErr(t, "load exact prerequisite", err)
	}
	if got := activation.Active("recovery"); len(got) != 2 || !got["mkdir"] || !got["write"] {
		t.Fatalf("unexpected activation=%v", got)
	}
	tc.Turn.TurnOfferedToolNames = []string{"command", "mkdir", "write"}
	direct := executor.classifyNativeRecovery(t.Context(), "implement", tc, candidates, true)
	if !reflect.DeepEqual(direct.calls, candidates) || len(direct.loadTools) != 0 {
		t.Fatal("ordered native recipe changed")
	}
	tc.Turn.TurnOfferedToolNames = []string{"command", "write"}
	missing := executor.classifyNativeRecovery(t.Context(), "implement", tc, candidates, true)
	if len(missing.calls) != 0 {
		t.Fatal("missing prerequisite emitted partial recipe")
	}
	// Schema failure must suppress the entire recipe, including earlier valid calls.
	meta, _ := registry.Meta("write")
	meta.ArgsSchema = map[string]any{"type": "object", "required": []string{"unprovided"}}
	testutil.FailErr(t, "replace current schema", registry.RegisterDerived(meta, noop))
	tc.Turn.TurnOfferedToolNames = []string{"command", "mkdir", "write"}
	if got := executor.classifyNativeRecovery(t.Context(), "implement", tc, candidates, true); len(got.calls) != 0 {
		t.Fatal("invalid later call left an executable prefix")
	}
}

func TestNativeRecoveryInvokeRendersCurrentFactsThroughProjection(t *testing.T) {
	bp := stockBlockPlane(t)
	boundary := sandbox.NewBoundary(fixtureSandboxConfig(), fixtureToolProfiles(t))
	registry := tools.NewDefaultRegistry()
	invoked := 0
	for _, name := range []string{"command", "write", "mkdir", "wait", "request_tools"} {
		testutil.FailErr(t, "register invocation fixture", registry.Register(name, func(context.Context, map[string]any, tools.ToolContext) (string, error) { invoked++; return "", nil }))
	}
	executor := NewExecutor(toolprofiles.NewProfilePolicyEngine(boundary), registry, "implement")
	executor.Rejections.SetBlockPlane(bp)
	for _, fixture := range []struct {
		name, command       string
		offered, deferred   []string
		load, omitted       bool
		incomplete, inexact bool
	}{
		{name: "direct", command: "sleep 2", offered: []string{"command", "wait"}},
		{name: "deferred", command: "sleep 2", offered: []string{"command", "request_tools"}, deferred: []string{"wait"}, load: true},
		{name: "write deferred", command: "cat > out.txt <<'EOF'\nhello\nEOF", offered: []string{"command", "request_tools"}, deferred: []string{"write"}, load: true},
		{name: "literal redirect", command: "echo built > out.log", offered: []string{"command", "write"}},
		{name: "literal redirect deferred", command: "echo built > out.log", offered: []string{"command", "request_tools"}, deferred: []string{"write"}, load: true},
		{name: "missing prerequisite", command: "mkdir -p sub && cat > sub/out.txt <<'EOF'\nhello\nEOF", offered: []string{"command", "write"}, incomplete: true},
		{name: "inexact write", command: "cat << EOF > out.txt\nUSER=$USER\nEOF", offered: []string{"command", "write"}, inexact: true},
		{name: "large exact write", command: "cat > out.txt <<'EOF'\n" + strings.Repeat("body", 12000) + "\nEOF", offered: []string{"command", "write"}, omitted: true},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			tc := fixtureToolContext(t.TempDir())
			tc.Turn.TurnOfferedToolNames = fixture.offered
			tc.Turn.TurnToolPlan = toolsurface.Compile(fixture.offered, fixture.deferred)
			// Context supplied by an earlier caller must not override Invoke's frozen surface.
			stale := tools.WithRecoveryTools(t.Context(), []string{"task", "write"})
			_, err := executor.Invoke(stale, "command", map[string]any{"command": fixture.command}, tc)
			refusal, ok := guidance.RefusalFromError(err)
			if !ok {
				t.Fatalf("missing rendered refusal: %v", err)
			}
			tr := toolrejection.AsToolReject(err)
			if tr == nil || tr.Data["tool_loadable"] != fixture.load {
				t.Fatalf("incorrect loading facts: %+v", tr)
			}
			fix, instead := refusal.Copy["fix"], refusal.Copy["instead"]
			if fixture.load {
				if !strings.Contains(fix, "containing only that name") || !strings.Contains(instead, "Load") {
					t.Fatalf("missing exact load remedy: %v", refusal.Copy)
				}
			} else if fixture.incomplete {
				if strings.Contains(fix, "Call `write`") || !strings.Contains(fix, "do not perform only part") || !strings.Contains(instead, "complete validated route") {
					t.Fatalf("partial recipe remedy: %v", refusal.Copy)
				}
			} else if fixture.inexact {
				if !strings.Contains(fix, "No complete replacement recipe was validated") || !strings.Contains(instead, "`write`") {
					t.Fatalf("missing inexact recovery: %v", refusal.Copy)
				}
			} else if !strings.Contains(fix, "If `replacement_calls` are present") || !strings.Contains(instead, "when present") {
				t.Fatalf("unconditional diagnostic promise: %v", refusal.Copy)
			}
			result := &api.ToolResult{Tool: "command", ToolCallID: "call", Feedback: refusal.Facts.Feedback}
			var wire strings.Builder
			for _, message := range transcript.Project([]api.Message{{ID: "result", Role: api.MessageRoleTool, Content: refusal.Body, ToolResult: result}}) {
				wire.WriteString(message.Content)
			}
			if strings.Contains(wire.String(), "details_omitted") != fixture.omitted {
				t.Fatalf("unexpected diagnostic projection omission=%t", fixture.omitted)
			}
		})
	}
	if invoked != 0 {
		t.Fatalf("rejection executed %d handlers", invoked)
	}
}
