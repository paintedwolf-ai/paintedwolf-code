package native_test

import (
	"context"
	"runtime"
	"testing"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/hostcmd"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
	"github.com/lycaon/lycaon/internal/tools/native/command"
	"github.com/lycaon/lycaon/pkg/api"
)

// Source-run consumers read the subsystem owner's invocation fact.

func withOut(tctx tools.ToolContext) tools.ToolContext {
	tctx.Out = &tools.ToolInvocationOut{}
	return tctx
}

func commandToolContext(root, sessionID, workerJobID string) tools.ToolContext {
	return tools.ToolContext{
		SessionID:   sessionID,
		WorkerJobID: workerJobID,
		Agent:       "implement",
		Roots:       []projectroot.RootRef{{ID: "primary", Path: root, IsPrimary: true}},
	}
}

func newCommandTool(t *testing.T) (*command.CommandTool, *bgprocess.Registry) {
	t.Helper()
	boundary := sandbox.NewBoundary(sandbox.Config{ProjectRootRequired: true}, []sandbox.ToolProfile{
		{ID: "implement", Tools: map[string]bool{"command": true}},
	})
	reg := bgprocess.NewRegistry(bgprocess.DefaultConfig(), bgprocess.Hooks{})
	return &command.CommandTool{
		Runner:     hostcmd.NewRunner(),
		Boundary:   boundary,
		Background: reg,
	}, reg
}

func TestCommandStatesVerdictOnSettledRun(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture is unix-oriented")
	}
	tool, _ := newCommandTool(t)
	tctx := withOut(commandToolContext(t.TempDir(), "sess", ""))
	_, err := tool.Run(context.Background(), map[string]any{"command": "echo hi"}, tctx)
	testutil.FailErr(t, "command run", err)

	run := tctx.Out.SourceRun
	if run == nil {
		t.Fatal("a settled command stated no source run")
	}
	if run.Verdict != api.SourceVerdictPassed {
		t.Fatalf("verdict = %q, want %q", run.Verdict, api.SourceVerdictPassed)
	}
	if run.ExitCode != 0 {
		t.Fatalf("exit code = %d, want 0", run.ExitCode)
	}
	if run.Command == "" {
		t.Fatal("stated run carries no command")
	}
}

func TestCommandStatesFailedVerdictOnNonZeroExit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture is unix-oriented")
	}
	tool, _ := newCommandTool(t)
	tctx := withOut(commandToolContext(t.TempDir(), "sess", ""))
	_, err := tool.Run(context.Background(), map[string]any{"command": "false"}, tctx)
	testutil.FailErr(t, "command run", err)

	run := tctx.Out.SourceRun
	if run == nil {
		t.Fatal("a settled command stated no source run")
	}
	if run.Verdict != api.SourceVerdictFailed {
		t.Fatalf("verdict = %q, want %q", run.Verdict, api.SourceVerdictFailed)
	}
}

// A launch that has not exited states nothing, which keeps a promoted handle
// out of a verification obligation.
func TestCommandStatesNoVerdictWhileRunning(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sleep fixture is unix-oriented")
	}
	tool, reg := newCommandTool(t)
	tctx := withOut(commandToolContext(t.TempDir(), "sess", ""))
	_, err := tool.Run(context.Background(), map[string]any{
		"command": "sleep 5",
		"wait_ms": float64(150),
	}, tctx)
	testutil.FailErr(t, "command run", err)

	if tctx.Out.SourceRun != nil {
		t.Fatalf("a promoted running command stated a verdict: %+v", tctx.Out.SourceRun)
	}
	testutil.FailErr(t, "dispose session commands", reg.DisposeSession(context.Background(), "sess"))
}

func TestVerifyStatesVerdictOnSettledRun(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture is unix-oriented")
	}
	tool := &native.VerifyTool{
		Runner:     hostcmd.NewRunner(),
		Background: bgprocess.NewRegistry(bgprocess.DefaultConfig(), bgprocess.Hooks{}),
	}
	tctx := withOut(commandToolContext(t.TempDir(), "sess", ""))
	tctx.Agent = "implement"
	_, err := tool.Run(context.Background(), map[string]any{"command": "true"}, tctx)
	testutil.FailErr(t, "verify run", err)

	run := tctx.Out.SourceRun
	if run == nil {
		t.Fatal("a settled verify stated no source run")
	}
	if run.Verdict != api.SourceVerdictPassed {
		t.Fatalf("verdict = %q, want %q", run.Verdict, api.SourceVerdictPassed)
	}
}
