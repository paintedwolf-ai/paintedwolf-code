package native_test

import (
	"context"
	"runtime"
	"testing"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/hostcmd"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
	"github.com/lycaon/lycaon/pkg/api"
)

// Source-run consumers read the subsystem owner's invocation fact.

func withOut(tctx tools.ToolContext) tools.ToolContext {
	tctx.Effects.Out = &tools.ToolInvocationOut{}
	return tctx
}

func commandToolContext(root, sessionID, workerJobID string) tools.ToolContext {
	return tools.ToolContext{
		Identity: tools.InvocationIdentity{SessionID: sessionID, WorkerJobID: workerJobID, Agent: "implement"},
		Source:   tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "primary", Path: root, IsPrimary: true}}},
	}
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
	tctx.Identity.Agent = "implement"
	_, err := tool.Run(context.Background(), map[string]any{"command": "true"}, tctx)
	testutil.FailErr(t, "verify run", err)

	run := tctx.Effects.Out.SourceRun
	if run == nil {
		t.Fatal("a settled verify stated no source run")
	}
	if run.Verdict != api.SourceVerdictPassed {
		t.Fatalf("verdict = %q, want %q", run.Verdict, api.SourceVerdictPassed)
	}
}
