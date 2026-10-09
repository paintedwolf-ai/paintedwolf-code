package native

import (
	"context"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/hostcmd"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/tools"
)

// confinedForeground is the shared boundary for agent-chosen commands.
type confinedForeground struct {
	Background     *bgprocess.Registry
	FailureTracker *CommandFailureTracker
	Runner         *hostcmd.Runner
	Boundary       *sandbox.Boundary
	WriteRootGate  SandboxWriteRootGate
	ToolName       string
}

// run returns a finished result or a promoted live handle. The command's
// source observation window spans the process: it closes with a finished
// command and rides a promoted one to its exit.
func (c confinedForeground) run(
	ctx context.Context,
	args map[string]any,
	tctx tools.ToolContext,
) (*hostcmd.Result, commandRunOutcome, error) {
	if reject := rejectDisallowedDesktopCapture(tctx, args); reject != nil {
		return nil, commandRunOutcome{}, reject
	}
	window := openCommandWindow(ctx, tctx, c.ToolName, canonicalCommandKey(tctx, args))
	outcome, err := c.dispatch(ctx, args, tctx)
	switch {
	case err != nil || outcome.Finished:
		window.closeNow(ctx)
	default:
		window.closeOnExit(ctx, c.Background, tctx.Identity.SessionID, outcome.Handle)
	}
	if err != nil || !outcome.Finished {
		return nil, outcome, err
	}
	res := commandResultFromOutcome(outcome, tools.LocalNetworkGrantOf(tctx), tctx.Files.PackageExecution)

	if err := rejectCommandNotFound(res, args); err != nil {
		outcome.IndexWatch.Release()
		return nil, outcome, err
	}
	if err := rejectCommandExecDenied(res, args); err != nil {
		outcome.IndexWatch.Release()
		return nil, outcome, err
	}

	appendBoundaryNotes(c.ToolName, tctx.Identity.SessionID, outcome, res)
	return res, outcome, nil
}

// stampBoundaryRefusal records a confinement refusal on the invocation.
func stampBoundaryRefusal(tctx tools.ToolContext, res *hostcmd.Result) {
	if tctx.Effects.Out == nil || res == nil {
		return
	}
	tctx.Effects.Out.Facts = tools.ApplyRefusalFacts(tctx.Effects.Out.Facts, confine.StampedRefusal{
		Attribution:   confine.FailureAttribution(res.BoundaryRefusal),
		GuidanceCodes: res.GuidanceCodes,
		Observation:   res.Observation,
	})
}

func (c confinedForeground) dispatch(
	ctx context.Context,
	args map[string]any,
	tctx tools.ToolContext,
) (commandRunOutcome, error) {
	if _, ok := args["terminal_capture"]; ok {
		return runCommandTerminalCapture(
			ctx, c.Background, c.FailureTracker, c.Runner, c.Boundary,
			args, tctx, c.sessionWriteRoots(ctx, tctx), c.ToolName,
		)
	}
	if _, ok := args["snapshot_capture"]; ok {
		return runCommandSnapshotCapture(
			ctx, c.Background, c.FailureTracker, c.Runner, c.Boundary,
			args, tctx, c.sessionWriteRoots(ctx, tctx), c.ToolName,
		)
	}
	return runCommandForeground(
		ctx, c.Background, c.FailureTracker, c.Runner, c.Boundary, args, tctx,
		commandWaitBudget(args), true, c.sessionWriteRoots(ctx, tctx), c.ToolName,
	)
}

// rejectDisallowedDesktopCapture refuses a parsed plan that runs a desktop
// capture program; an unparsable plan is left to the handler's own refusal.
func rejectDisallowedDesktopCapture(tctx tools.ToolContext, args map[string]any) error {
	plan, err := tctx.CommandPlan(args)
	if err == nil {
		for _, stage := range plan.Stages {
			base := strings.ToLower(filepath.Base(strings.TrimSpace(stage.Name)))
			if base == "screencapture" || base == "scrot" || base == "xwd" {
				return toolrejection.RejectInvalidArguments("DESKTOP_CAPTURE_DISALLOWED", map[string]any{
					"command": canonicalCommandKey(tctx, args),
					"reason":  "desktop_screen_capture_disallowed",
					"instead": "Use command with snapshot_capture or APP_SNAPSHOT to render views offscreen in-process without OS permissions",
				})
			}
		}
	}
	return nil
}

// sessionWriteRoots is the chat write-root overlay; empty without a gate.
func (c confinedForeground) sessionWriteRoots(ctx context.Context, tctx tools.ToolContext) []string {
	if c.WriteRootGate == nil {
		return nil
	}
	return c.WriteRootGate.SessionWriteRoots(ctx, tctx.Identity.SessionID, tctx.Identity.ParentSessionID)
}
