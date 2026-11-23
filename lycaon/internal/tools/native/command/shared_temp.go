package command

import (
	"context"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/tools"
)

// SharedTempReviewer settles the distinct shared locations in a foreground result.
type SharedTempReviewer interface {
	ReviewSharedTemp(context.Context, SandboxWriteRootAsk, []string, bool) guidance.SharedTempSettlement
}

func (r *ProgramRuntime) reviewSharedTemp(ctx context.Context, tc tools.ToolContext, tool, source string, running bool) {
	out := tc.Effects.Out
	if out == nil {
		return
	}
	obs := out.Facts.Confine
	if !obs.Applied {
		return
	}
	roots := obs.Refusals.SharedTempRoots()
	if len(roots) == 0 {
		return
	}
	result := guidance.SharedTempSettlement{Refused: roots}
	switch {
	case running || obs.Running:
		result.Running = true
	case obs.TerminationReason != "" && obs.TerminationReason != string(bgprocess.TerminationExited):
		return
	case r != nil && r.SharedTemp != nil && (len(obs.FailedStages) > 0 || obs.TerminationReason == string(bgprocess.TerminationExited)):
		result = r.SharedTemp.ReviewSharedTemp(ctx, SandboxWriteRootAsk{
			SessionID: tc.Identity.SessionID, ParentSessionID: tc.Identity.ParentSessionID, ProjectID: tc.Identity.ProjectID,
			ToolCallID: tc.Identity.ToolCallID, ProjectDir: tc.ActiveRootPath(), ToolName: tool, Command: source,
			SessionScratchRoot: tc.Host.SessionScratchDir, SessionOutputRoot: tc.Host.SessionOutputDir,
		}, roots, len(obs.FailedStages) > 0)
	default:
		return
	}
	result.ScratchAvailable = tc.Host.SessionScratchDir != ""
	out.Facts.SharedTemp = &result
}

func (e *programEvaluation) observeRunningConfinement() {
	if out := e.callTC.Effects.Out; out != nil {
		e.run.tc.Effects.Out.WithLock(func(child *tools.ToolInvocationOut) {
			out.Facts.Confine = child.Facts.Confine
			out.Facts.Confine.Running = true
		})
	}
}
