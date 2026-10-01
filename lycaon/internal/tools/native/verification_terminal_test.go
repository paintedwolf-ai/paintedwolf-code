package native

import (
	"testing"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/hostcmd"
	"github.com/lycaon/lycaon/internal/tools"
)

func TestVerificationCanceledZeroExitIsNotPass(t *testing.T) {
	for _, reason := range []bgprocess.TerminationReason{bgprocess.TerminationStopped, bgprocess.TerminationTimedOut} {
		result := commandResultFromOutcome(commandRunOutcome{Snapshot: bgprocess.Snapshot{HasExit: true, ExitCode: 0, TerminationReason: reason}}, confine.LocalNetworkGrant{}, nil)
		verdict, _ := verdictFor(result)
		if verdict == VerifyOutcomePassed || result.OK {
			t.Fatalf("%s produced a pass", reason)
		}
	}
}

func TestVerificationReceiptRetainsLaunchIdentity(t *testing.T) {
	out := &tools.ToolInvocationOut{}
	ctx := tools.ToolContext{ToolCallID: "check-1", Out: out}
	stampSourceRun(ctx, &hostcmd.Result{Stages: []hostcmd.StageResult{{Command: "project-check"}}, ExitCode: 0}, VerifyOutcomePassed,
		commandRunOutcome{IsCheck: true, SourceRevision: "launch-content", SourceRootDigest: "launch-root", Cwd: "."})
	if out.SourceRun == nil || out.SourceRun.SourceRevision != "launch-content" || out.SourceRun.CheckID != "check-1" || !out.SourceRun.IsCheck {
		t.Fatalf("receipt=%+v", out.SourceRun)
	}
}

func TestVerificationTerminalCaptureTimeoutIsNotPass(t *testing.T) {
	snapshot := terminalCaptureSnapshot(bgprocess.PTYCaptureResult{ExitCode: 0, TimedOut: true}, "project-check", "")
	result := commandResultFromOutcome(commandRunOutcome{Snapshot: snapshot}, confine.LocalNetworkGrant{}, nil)
	verdict, _ := verdictFor(result)
	if result.OK || verdict == VerifyOutcomePassed {
		t.Fatal("timed out terminal check produced a pass")
	}
}
