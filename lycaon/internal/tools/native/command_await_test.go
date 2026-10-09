package native_test

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/hostcmd"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
)

func commandToolContext(root, sessionID, workerJobID string) tools.ToolContext {
	return tools.ToolContext{
		Identity: tools.InvocationIdentity{SessionID: sessionID,
			WorkerJobID: workerJobID,
			Agent:       "implement"},
		Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "primary", Path: root, IsPrimary: true}}},
	}
}

func newCommandTool(t *testing.T) (*native.CommandTool, *bgprocess.Registry) {
	t.Helper()
	boundary := sandbox.NewBoundary(sandbox.Config{ProjectRootRequired: true}, []sandbox.ToolProfile{
		{ID: "implement", Tools: map[string]bool{"command": true}},
	})
	reg := bgprocess.NewRegistry(bgprocess.DefaultConfig(), bgprocess.Hooks{})
	return &native.CommandTool{
		Runner:     hostcmd.NewRunner(),
		Boundary:   boundary,
		Background: reg,
	}, reg
}

type oneShotDirectIPRuntime struct {
	consumed bool
}

func (*oneShotDirectIPRuntime) IssuePermit(string, string, string, string, string) {}

func (*oneShotDirectIPRuntime) LeaseCovers(string, hitl.DirectIPLease) bool { return false }

func (*oneShotDirectIPRuntime) GrantChat(string, hitl.DirectIPLease, string, string, *time.Time) {}

func (r *oneShotDirectIPRuntime) ConsumePermit(string, string, string, string, string) (bool, error) {
	if r.consumed {
		return false, nil
	}
	r.consumed = true
	return true, nil
}

func (r *oneShotDirectIPRuntime) Authorized(string, string, string) bool {
	return !r.consumed
}

func TestCommandForegroundCompletesInline(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture is unix-oriented")
	}
	tool, reg := newCommandTool(t)
	root := t.TempDir()
	out, err := tool.Run(context.Background(), map[string]any{"command": "echo hi"}, commandToolContext(root, "sess", ""))
	if err != nil {
		t.Fatalf("command run: %v", err)
	}
	if strings.Contains(out, "\"running\":true") {
		t.Fatalf("fast command should complete inline, got running payload: %s", out)
	}
	var res map[string]any
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if ok, _ := res["ok"].(bool); !ok {
		t.Fatalf("expected ok result, got %s", out)
	}
	if reg.HasRunning("sess") {
		t.Fatal("inline-completed command must not leave a running handle")
	}
}

func TestDirectIPLifecycleDoesNotClaimUnavailableSubstrate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture is unix-oriented")
	}
	tool, _ := newCommandTool(t)
	root := t.TempDir()
	tctx := commandToolContext(root, "sess-direct", "")
	tctx.Identity.ToolCallID = "call-direct"
	tctx.Direct.DirectIPRequested = true
	tctx.Direct.DirectIPAuthorized = true
	tctx.Direct.DirectIPActionDigest = "action"
	tctx.Direct.DirectIPRequestDigest = "request"
	tctx.Direct.DirectIPConfineDigest = "confinement"
	tctx.Direct.DirectIPCapabilityRuntime = &oneShotDirectIPRuntime{}
	var phases []tools.DirectIPLifecyclePhase
	tctx.Direct.DirectIPLifecycle = func(event tools.DirectIPLifecycleEvent) {
		phases = append(phases, event.Phase)
	}

	if _, err := tool.Run(context.Background(), map[string]any{"command": "echo hi"}, tctx); err != nil {
		t.Fatalf("command run: %v", err)
	}
	if len(phases) != 0 {
		t.Fatalf("unavailable confinement reported direct-IP lifecycle: %v", phases)
	}
}

func TestCommandForegroundPromotesOnBudget(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sleep fixture is unix-oriented")
	}
	tool, reg := newCommandTool(t)
	root := t.TempDir()
	out, err := tool.Run(context.Background(), map[string]any{
		"command": "sleep 5",
		"wait_ms": float64(150),
	}, commandToolContext(root, "sess", ""))
	if err != nil {
		t.Fatalf("command run: %v", err)
	}
	var res map[string]any
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if running, _ := res["running"].(bool); !running {
		t.Fatalf("slow command should hand back a running handle, got %s", out)
	}
	if handle, _ := res["handle"].(string); handle == "" {
		t.Fatalf("running payload must carry a handle, got %s", out)
	}
	if !reg.HasRunning("sess") {
		t.Fatal("promoted command must still be running")
	}
	testutil.FailErr(t, "dispose session commands", reg.Lifecycle.DisposeSession(context.Background(), "sess"))
}

func TestCommandForegroundCancellationStopsSilentJob(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sleep fixture is unix-oriented")
	}
	tool, reg := newCommandTool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := tool.Run(ctx, map[string]any{"command": "sleep 5"}, commandToolContext(t.TempDir(), "canceled", ""))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("canceled command error = %v, want deadline exceeded", err)
	}
	if reg.HasRunning("canceled") {
		t.Fatal("canceled foreground command was still running when the tool returned")
	}
	if jobs := reg.ActiveJobs("canceled"); len(jobs) != 0 {
		t.Fatalf("canceled foreground jobs = %+v, want none", jobs)
	}
}

func TestCommandForegroundReturnsStructuredLiveJobConflicts(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sleep fixture is unix-oriented")
	}
	tool, reg := newCommandTool(t)
	root := t.TempDir()
	tctx := commandToolContext(root, "sess", "")
	_, err := tool.Run(context.Background(), map[string]any{
		"command": "sleep 5", "wait_ms": float64(50),
	}, tctx)
	testutil.FailErr(t, "start first awaited command", err)
	t.Cleanup(func() {
		testutil.FailErr(t, "dispose session commands", reg.Lifecycle.DisposeSession(context.Background(), "sess"))
	})

	_, err = tool.Run(context.Background(), map[string]any{
		"command": "sleep 4", "wait_ms": float64(50),
	}, tctx)
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject.Code != "COMMAND_IN_FLIGHT" {
		t.Fatalf("second awaited command error = %#v want COMMAND_IN_FLIGHT", err)
	}
	_, err = tool.Run(context.Background(), map[string]any{
		"command": "sleep 5", "wait_ms": float64(50), "allow_concurrent": true,
	}, tctx)
	if !errors.As(err, &reject) || reject.Code != "COMMAND_DUPLICATE_RUNNING" {
		t.Fatalf("duplicate command error = %#v want COMMAND_DUPLICATE_RUNNING", err)
	}
}

// env.PWD that matches the resolved cwd is allowed as a plain env variable.
func TestCommandAllowsEnvPwdMatchingCwd(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture is unix-oriented")
	}
	tool, _ := newCommandTool(t)
	root := t.TempDir()
	out, err := tool.Run(context.Background(), map[string]any{
		"command": "echo hi",
		"env":     map[string]any{"PWD": root},
	}, commandToolContext(root, "sess", ""))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "hi") {
		t.Fatalf("expected output hi, got: %s", out)
	}
}

// env.PWD that differs from the resolved cwd is rejected; it is not a cwd mechanism.
func TestCommandRejectsEnvPwdMismatch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture is unix-oriented")
	}
	tool, _ := newCommandTool(t)
	root := t.TempDir()
	_, err := tool.Run(context.Background(), map[string]any{
		"command": "echo hi",
		"env":     map[string]any{"PWD": "/elsewhere"},
	}, commandToolContext(root, "sess", ""))
	if err == nil {
		t.Fatal("expected env.PWD mismatch reject")
	}
	if !strings.Contains(err.Error(), "COMMAND_PWD_NOT_CWD") {
		t.Fatalf("expected COMMAND_PWD_NOT_CWD, got: %v", err)
	}
}

// The same failing command three times in a row triggers a hard failure-loop reject.
func TestCommandFailureLoopBlocksRepeatedFailures(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture is unix-oriented")
	}
	tool, _ := newCommandTool(t)
	root := t.TempDir()
	ctx := commandToolContext(root, "sess", "")
	cmd := map[string]any{"command": "false"}
	for i := 0; i < 3; i++ {
		out, err := tool.Run(context.Background(), cmd, ctx)
		if err != nil {
			t.Fatalf("iteration %d: unexpected tool error: %v", i, err)
		}
		var res map[string]any
		if err := json.Unmarshal([]byte(out), &res); err != nil {
			t.Fatalf("iteration %d: unmarshal: %v", i, err)
		}
		if ok, _ := res["ok"].(bool); ok {
			t.Fatalf("iteration %d: expected failing command, got ok", i)
		}
	}
	_, err := tool.Run(context.Background(), cmd, ctx)
	if err == nil {
		t.Fatal("expected COMMAND_FAILURE_LOOP after 3 repeated failures")
	}
	if !strings.Contains(err.Error(), "COMMAND_FAILURE_LOOP") {
		t.Fatalf("expected COMMAND_FAILURE_LOOP, got: %v", err)
	}
}

// A sandbox Code that names a field is a new approach, not a replay.
func TestCommandFailureLoopResetsWhenCapabilityChanges(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture is unix-oriented")
	}
	tool, _ := newCommandTool(t)
	root := t.TempDir()
	ctx := commandToolContext(root, "sess-cap", "")
	cmd := map[string]any{"command": "false"}
	for i := 0; i < 3; i++ {
		out, err := tool.Run(context.Background(), cmd, ctx)
		if err != nil {
			t.Fatalf("iteration %d: unexpected tool error: %v", i, err)
		}
		var res map[string]any
		if err := json.Unmarshal([]byte(out), &res); err != nil {
			t.Fatalf("iteration %d: unmarshal: %v", i, err)
		}
		if ok, _ := res["ok"].(bool); ok {
			t.Fatalf("iteration %d: expected failing command, got ok", i)
		}
	}
	retried, err := tool.Run(context.Background(), map[string]any{
		"command":     "false",
		"socks_proxy": true,
	}, ctx)
	if err != nil {
		t.Fatalf("socks_proxy must reset the failure loop: %v", err)
	}
	var res map[string]any
	if err := json.Unmarshal([]byte(retried), &res); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if ok, _ := res["ok"].(bool); ok {
		t.Fatal("expected failing command after the new approach")
	}
}
