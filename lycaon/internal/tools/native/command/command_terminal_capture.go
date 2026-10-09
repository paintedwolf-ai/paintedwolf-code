package command

import (
	"context"
	"fmt"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"strings"

	"github.com/lycaon/lycaon/internal/argv"
	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/confine"
	lycexec "github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/hostcmd"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

func runCommandTerminalCapture(
	ctx context.Context,
	registry *bgprocess.Registry,
	tracker *CommandFailureTracker,
	runner *hostcmd.Runner,
	boundary *sandbox.Boundary,
	args map[string]any,
	tctx tools.ToolContext,
	extraWriteRoots []string,
	toolName string,
) (RunOutcome, error) {
	if toolName != "command" {
		return RunOutcome{}, toolrejection.RejectInvalidArguments("TOOL_ARGS_INVALID", map[string]any{"reason": "terminal_capture_is_command_only"})
	}
	for _, key := range []string{"snapshot_capture", "pipeline", "stdin", "stdin_from", "stdout_to", "stderr_to", "background"} {
		if value, exists := args[key]; exists && value != nil && value != false && value != "" {
			return RunOutcome{}, toolrejection.RejectInvalidArguments("TOOL_ARGS_INVALID", map[string]any{"reason": "terminal_capture_incompatible", "field": key})
		}
	}
	plan, err := tctx.CommandPlan(args)
	if err != nil {
		return RunOutcome{}, err
	}
	stages := plan.Stages
	if len(stages) != 1 {
		// A sealed screen captures one process, not a sequence.
		return RunOutcome{}, commandsurface.ErrSequenceUnsupported
	}
	if !stages[0].Streams.Terminal() {
		// The terminal owns stdin, stdout, and stderr; a file redirection has nowhere to attach.
		return RunOutcome{}, &argv.RedirectionError{Issue: argv.IssueSurfaceUnsupported, Operator: argv.RenderRedirects(stages[0].Redirects)}
	}
	ioParams, err := CommandIO(ctx, boundary, tctx, plan, args, toolName)
	if err != nil {
		return RunOutcome{}, err
	}
	cwdArg, _ := args["cwd"].(string)
	cwd, cwdDisplay, err := projectpaths.CommandCwd(ctx, tctx, cwdArg)
	if err != nil {
		return RunOutcome{}, err
	}
	if err := tools.ValidateWorkerBranch(ctx, tctx); err != nil {
		return RunOutcome{}, err
	}
	if reject := rejectPwdEnvMismatch(args, canonicalCommandKey(tctx, args), cwd); reject != nil {
		return RunOutcome{}, reject
	}
	commandLine := canonicalCommandKey(tctx, args)
	approach := commandFailureApproachKey(canonicalToolArgs(tctx, args))
	if reject := tracker.rejectLoop(tctx.Identity.SessionID, approach, commandLine); reject != nil {
		return RunOutcome{}, reject
	}
	if err := runner.ValidateStages(ctx, stages); err != nil {
		return RunOutcome{}, err
	}
	bound, err := bindCommandForegroundConfine(ctx, tctx, extraWriteRoots, commandLine, toolName, true)
	if err != nil {
		return RunOutcome{}, err
	}
	if registry == nil {
		bound.close(ctx)
		return RunOutcome{}, fmt.Errorf("background registry not configured")
	}
	directIPApplied := bound.directIPApplied
	if directIPApplied {
		tools.EmitDirectIPLifecycle(tctx, tools.DirectIPLifecycleStarted)
	}
	request := hostcmd.Request{
		Launch:     agentCommandLaunch(tctx, toolName, bound.confinement),
		ProjectDir: cwd, ProfileID: tctx.ProfileID(), Stages: stages, IOParams: ioParams,
		PathExtra: append([]string(nil), tctx.Host.HostResourcePathExtra...),
	}
	facts := confine.SpawnFacts{
		Report: confine.ReportOf(confine.BoundaryOf(bound.confinement)).
			WithLocalNetwork(tools.LocalNetworkGrantOf(tctx)),
		Action: bound.lease,
	}
	if tctx.Files.PackageExecution != nil {
		facts.Report = facts.Report.WithRemotePackageExecution(tctx.Files.PackageExecution.AllowedHosts, tctx.Files.PackageExecution.ApprovedReadPaths)
	}
	if bound.lease != nil {
		facts.Network = bound.lease.ObservedHosts
	}
	if err := tctx.Effects.Secrets.HandOff(ctx, nil); err != nil {
		return RunOutcome{}, toolrejection.HeldHandOffReject(toolName, err)
	}
	var sourceRevision, sourceRootDigest string
	if tctx.Execution.VerificationCheck {
		sourceRevision, sourceRootDigest = sourceledger.VerificationState(ctx, tctx.Source.SourceLedger, tools.HostWriteRoot(tctx))
	}
	captured, runErr := registry.Terminal.RunPTYCapture(
		ctx, tctx.Identity.SessionID, tctx.Identity.ParentSessionID, tctx.Identity.ProjectID, request, runner,
		terminalCaptureWinSize(args), facts,
		commandTimeout(args, toolName),
	)
	if directIPApplied {
		tools.EmitDirectIPLifecycle(tctx, tools.DirectIPLifecycleCompleted)
	}
	var network []confine.EgressHost
	if bound.lease != nil {
		network = bound.close(ctx)
		tools.RecordMediatedEgress(ctx, tctx, toolName, network)
	}
	refusals := bound.lease.SettledRefusals(ctx)
	if runErr != nil {
		return RunOutcome{}, runErr
	}
	tools.CaptureExternalAccess(tctx, network, directIPApplied)
	tail := terminalScreenText(captured.Screen)
	tracker.record(tctx.Identity.SessionID, approach, captured.ExitCode == 0 && !captured.TimedOut)
	return RunOutcome{
		IsCheck:        tctx.Execution.VerificationCheck,
		SourceRevision: sourceRevision, SourceRootDigest: sourceRootDigest,
		Finished: true,
		Snapshot: TerminalCaptureSnapshot(captured, stages[0].EchoLine(), tail),
		Boundary: captured.Boundary, Network: network, Refusals: refusals, IO: ioParams, Cwd: cwdDisplay,
		Terminal: &captured,
	}, nil
}

func terminalCaptureWinSize(args map[string]any) lycexec.WinSize {
	config, _ := args["terminal_capture"].(map[string]any)
	winsize, _ := config["winsize"].(map[string]any)
	cols, _ := winsize["cols"].(float64)
	rows, _ := winsize["rows"].(float64)
	return lycexec.WinSize{Cols: uint16(cols), Rows: uint16(rows)}
}

func terminalCaptureCaption(args map[string]any) string {
	config, _ := args["terminal_capture"].(map[string]any)
	caption, _ := config["caption"].(string)
	return strings.TrimSpace(caption)
}

func terminalScreenText(screen bgprocess.ScreenSnapshot) string {
	lines := make([]string, 0, len(screen.Lines))
	for _, line := range screen.Lines {
		lines = append(lines, strings.TrimRight(line, " "))
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func TerminalCaptureSnapshot(captured bgprocess.PTYCaptureResult, command, tail string) bgprocess.Snapshot {
	reason := bgprocess.TerminationExited
	if captured.TimedOut {
		reason = bgprocess.TerminationTimedOut
	}
	return bgprocess.Snapshot{
		HasExit: true, ExitCode: captured.ExitCode, Tail: tail, TerminationReason: reason,
		Stages: []hostcmd.StageResult{{Command: command, ExitCode: &captured.ExitCode, Failed: captured.ExitCode != 0}},
	}
}
