package native

import (
	"testing"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/hostcmd"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/command"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestVerificationCanceledZeroExitIsNotPass(t *testing.T) {
	for _, reason := range []bgprocess.TerminationReason{bgprocess.TerminationStopped, bgprocess.TerminationTimedOut} {
		result := command.CommandResultFromOutcome(command.RunOutcome{Snapshot: bgprocess.Snapshot{HasExit: true, ExitCode: 0, TerminationReason: reason}}, confine.LocalNetworkGrant{}, nil)
		verdict, _ := command.VerdictFor(result)
		if verdict == api.SourceVerdictPassed || result.OK {
			t.Fatalf("%s produced a pass", reason)
		}
	}
}

func TestVerificationReceiptRetainsLaunchIdentity(t *testing.T) {
	out := &tools.ToolInvocationOut{}
	ctx := tools.ToolContext{
		Identity: tools.InvocationIdentity{ToolCallID: "check-1"},
		Effects:  tools.InvocationEffects{Out: out},
	}
	command.StampSourceRun(ctx, &hostcmd.Result{Stages: []hostcmd.StageResult{{Command: "project-check"}}, ExitCode: 0}, api.SourceVerdictPassed,
		command.RunOutcome{IsCheck: true, SourceRevision: "launch-content", SourceRootDigest: "launch-root", Cwd: "."})
	if out.SourceRun == nil || out.SourceRun.SourceRevision != "launch-content" || out.SourceRun.CheckID != "check-1" || !out.SourceRun.IsCheck {
		t.Fatalf("receipt=%+v", out.SourceRun)
	}
}

func TestVerificationTerminalCaptureTimeoutIsNotPass(t *testing.T) {
	snapshot := command.TerminalCaptureSnapshot(bgprocess.PTYCaptureResult{ExitCode: 0, TimedOut: true}, "project-check", "")
	result := command.CommandResultFromOutcome(command.RunOutcome{Snapshot: snapshot}, confine.LocalNetworkGrant{}, nil)
	verdict, _ := command.VerdictFor(result)
	if result.OK || verdict == api.SourceVerdictPassed {
		t.Fatal("timed out terminal check produced a pass")
	}
}
