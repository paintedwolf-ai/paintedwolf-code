package command

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/hostcmd"
	"github.com/lycaon/lycaon/internal/packageexec"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/tooloutput"
	"github.com/lycaon/lycaon/internal/tools"
	terminaltool "github.com/lycaon/lycaon/internal/tools/native/terminal"
	"github.com/lycaon/lycaon/internal/tools/native/toolkit"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
	"github.com/lycaon/lycaon/pkg/api"
)

// DeclaredVerifyCommand resolves the project's verification command.
type DeclaredVerifyCommand func(projectDir string) string

// CommandTool executes commands within the invocation's confinement boundary.
type CommandTool struct {
	DeclaredCommand DeclaredVerifyCommand
	Runner          *hostcmd.Runner
	Boundary        *sandbox.Boundary
	Background      *bgprocess.Registry
	FailureTracker  *CommandFailureTracker
	WriteRootGate   SandboxWriteRootGate
}

func (t *CommandTool) Run(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
	tctx.VerificationCheck = VerificationRequested(args)
	if t.DeclaredCommand != nil {
		declared := t.DeclaredCommand(tctx.ActiveRootPath())
		tctx.VerificationCheck = tctx.VerificationCheck || (strings.TrimSpace(declared) != "" && commandsurface.SameCommandLine(CanonicalCommandKey(tctx, args), declared))
	}
	if t.Runner == nil {
		return "", fmt.Errorf("command runner not configured")
	}
	if t.FailureTracker == nil {
		t.FailureTracker = NewCommandFailureTracker()
	}
	if toolkit.BoolArg(args, "background", false) {
		return RunBackground(ctx, t.Background, t.Runner, t.Boundary, args, tctx, t.confined().SessionWriteRoots(ctx, tctx), "command")
	}
	res, outcome, err := t.confined().Run(ctx, args, tctx)
	if err != nil {
		return "", err
	}
	if !outcome.Finished {
		return EncodeCommandRunning(tctx, outcome, WaitBudget(args))
	}
	StampBoundaryRefusal(tctx, res)
	StampIndexWatch(tctx, outcome.IndexWatch)
	commandVerdict, _ := VerdictFor(res)
	StampSourceRun(tctx, res, commandVerdict, outcome)
	var payload any = res
	if outcome.Terminal != nil {
		caption := terminalCaptureCaption(args)
		capture := terminaltool.CaptureFromScreen(ctx, t.Background, tctx, outcome.Terminal.Screen, caption)
		payload = struct {
			*hostcmd.Result
			TerminalCapture terminaltool.SnapshotResult `json:"terminal_capture"`
		}{Result: res, TerminalCapture: capture}
	} else if outcome.SnapshotCapture != nil {
		payload = struct {
			*hostcmd.Result
			SnapshotCapture *SnapshotCaptureResult `json:"snapshot_capture"`
		}{Result: res, SnapshotCapture: outcome.SnapshotCapture}
	}
	out, err := surveyjson.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("command encode: %w", err)
	}
	return string(out), nil
}

// confined binds this tool's wiring to the shared confined-foreground path.
func (t *CommandTool) confined() ConfinedForeground {
	return ConfinedForeground{
		Background:     t.Background,
		FailureTracker: t.FailureTracker,
		Runner:         t.Runner,
		Boundary:       t.Boundary,
		WriteRootGate:  t.WriteRootGate,
		ToolName:       "command",
	}
}

// BoundaryNetworkMode returns the applied egress label.
func BoundaryNetworkMode(b confine.Boundary) string {
	if !b.Applied {
		return ""
	}
	return confine.NetworkLabel(b.Network)
}

// CommandConfinementReport builds the process boundary report.
func CommandConfinementReport(
	boundary confine.Boundary, posture string, local confine.LocalNetworkGrant,
	packageExecution *packageexec.Execution,
) confine.Report {
	report := confine.ReportOf(boundary)
	report.NetworkPosture = posture
	report = report.WithLocalNetwork(local)
	if packageExecution != nil {
		report = report.WithRemotePackageExecution(packageExecution.AllowedHosts, packageExecution.ApprovedReadPaths)
	}
	return report
}

// CommandResultFromOutcome builds a finished foreground result.
func CommandResultFromOutcome(
	outcome RunOutcome,
	local confine.LocalNetworkGrant,
	packageExecution *packageexec.Execution,
) *hostcmd.Result {
	res := &hostcmd.Result{
		Stages:            outcome.Snapshot.Stages,
		ExitCode:          outcome.Snapshot.ExitCode,
		Tail:              outcome.Snapshot.Tail,
		OK:                outcome.Snapshot.ExitCode == 0 && outcome.Snapshot.Failure == nil && outcome.Snapshot.TerminationReason != bgprocess.TerminationStopped && outcome.Snapshot.TerminationReason != bgprocess.TerminationTimedOut,
		ExecFailure:       outcome.Snapshot.Failure,
		TerminationReason: string(outcome.Snapshot.TerminationReason),
		Network:           outcome.Network,
		Report:            CommandConfinementReport(outcome.Boundary, outcome.NetworkPosture, local, packageExecution).WithSandboxRefusals(outcome.Refusals),
		StdinProvided:     outcome.IO.StdinProvided,
		StdinFrom:         outcome.IO.StdinFrom,
		StdoutTo:          outcome.IO.StdoutTo,
		StderrTo:          outcome.IO.StderrTo,
		EnvKeys:           outcome.IO.EnvKeys,
		Cwd:               outcome.Cwd,
		LeftRunning:       outcome.LeftRunning,
	}
	res.Tail = enginepaths.RewriteWorkerBranchPaths(res.Tail) // compilers print the absolute cwd
	if snap := outcome.Snapshot; len(snap.Output) > len(snap.Tail) || snap.OutputEvicted {
		res.Truncated = true
		res.OriginalTailBytes = len(snap.Output)
		res.WireSpillPath = outcome.SpillPath
	}
	if tail, truncated, orig := capOpaqueTail(res.Tail, 0); truncated {
		res.Tail = tail
		res.Truncated = true
		res.OriginalTailBytes = max(res.OriginalTailBytes, orig)
	}
	return res
}

func capOpaqueTail(tail string, maxBytes int) (capped string, truncated bool, originalBytes int) {
	originalBytes = len(tail)
	cap := tooloutput.EffectiveMaxSpillFileBytes(maxBytes)
	if originalBytes <= cap {
		return tail, false, originalBytes
	}
	return tooloutput.CapSpillBytes(tail, maxBytes), true, originalBytes
}

// captureProcessHandle records a live process on the invocation.
func captureProcessHandle(tctx tools.ToolContext, handle string, running bool) {
	if tctx.Out == nil || strings.TrimSpace(handle) == "" {
		return
	}
	tctx.Out.Process = &api.ToolProcessHandle{Handle: handle, Running: running}
}

// EncodeCommandRunning renders a foreground command promoted to a live handle,
// with what the kernel has refused it so far.
func EncodeCommandRunning(tctx tools.ToolContext, outcome RunOutcome, budget time.Duration) (string, error) {
	captureProcessHandle(tctx, outcome.Handle, true)
	report := CommandConfinementReport(
		outcome.Boundary, outcome.NetworkPosture, tools.LocalNetworkGrantOf(tctx), tctx.PackageExecution,
	)
	if outcome.Boundary.Applied {
		stamped := confine.StampRefusal("command", tctx.SessionID, outcome.Boundary, confine.RefusalContext{
			MediatedNetwork:        outcome.Network,
			RemotePackageExecution: report.RemotePackageExecution,
			Running:                true,
			Refusals:               outcome.Refusals,
		})
		report.BoundaryRefusal = string(stamped.Attribution)
		if tctx.Out != nil {
			tctx.Out.Facts = tools.ApplyRefusalFacts(tctx.Out.Facts, stamped)
		}
	}
	out, err := surveyjson.Marshal(hostcmd.CommandRunningResult{
		Running:  true,
		Handle:   outcome.Handle,
		Stages:   outcome.Snapshot.Stages,
		Tail:     outcome.Snapshot.Tail,
		WaitedMs: int(budget.Milliseconds()),
		Network:  outcome.Network,
		Report:   report.WithSandboxRefusals(outcome.Refusals),
	})
	if err != nil {
		return "", fmt.Errorf("command running encode: %w", err)
	}
	return string(out), nil
}

// VerificationRequested reports whether verification check mode was passed in args.
func VerificationRequested(args map[string]any) bool {
	requested, _ := args["verification"].(bool)
	return requested
}

// VerdictFor maps execution termination to verification outcome.
func VerdictFor(res *hostcmd.Result) (string, string) {
	if res == nil {
		return api.SourceVerdictUnverifiable, "boundary_refused"
	}
	if res.BoundaryRefusal != "" {
		return api.SourceVerdictUnverifiable, "boundary_refused"
	}
	switch res.TerminationReason {
	case string(bgprocess.TerminationTimedOut):
		return api.SourceVerdictUnverifiable, "host_deadline"
	case string(bgprocess.TerminationStopped):
		return api.SourceVerdictUnverifiable, "host_stopped"
	}
	if res.OK {
		return api.SourceVerdictPassed, ""
	}
	return api.SourceVerdictFailed, ""
}

// StampSourceRun attaches terminal evidence to the invocation receipt.
func StampSourceRun(tctx tools.ToolContext, res *hostcmd.Result, verdict string, outcome RunOutcome) {
	if tctx.Out == nil || res == nil {
		return
	}
	tctx.Out.SourceRun = &tools.SourceRunCapture{
		CheckID: tctx.ToolCallID, IsCheck: outcome.IsCheck,
		Command:        hostcmd.CommandLine(res.Stages),
		ExitCode:       res.ExitCode,
		Verdict:        verdict,
		SourceRevision: outcome.SourceRevision, SourceRootDigest: outcome.SourceRootDigest, Cwd: outcome.Cwd,
	}
}
