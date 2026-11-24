package command

import (
	"context"

	awaitstore "github.com/lycaon/lycaon/internal/await"
	"github.com/lycaon/lycaon/internal/tools"
)

// DerivedCommandHandoff carries the derived one-shot watch registration for an unjoined job or continued program.
type DerivedCommandHandoff struct {
	Handle        string
	WatchHandle   string
	WaitCondition awaitstore.Condition
	OutputCursor  int64
}

// DerivedWatchRegistrar registers derived command observations and reconciles shell joins.
type DerivedWatchRegistrar interface {
	HandoffCommand(ctx context.Context, tc tools.ToolContext, commandHandle string, outputCursor int64) (DerivedCommandHandoff, bool, error)
	ReconcileShellJoin(ctx context.Context, tc tools.ToolContext, commandHandle string) error
}

// SetDerivedWatches wires derived watch handoff and shell join reconciliation onto the runtime and supervisor.
func (r *ProgramRuntime) SetDerivedWatches(registrar DerivedWatchRegistrar) {
	if r == nil {
		return
	}
	r.DerivedWatches = registrar
	if r.Supervisor != nil {
		r.Supervisor.SetDerivedWatches(registrar)
	}
}

func (r *ProgramRuntime) handoffBackgroundJobs(ctx context.Context, e *programEvaluation, outcome *ProgramOutcome) {
	if r == nil || e == nil || outcome == nil || r.Supervisor == nil {
		return
	}
	jobs := r.Supervisor.Jobs(e.callTC.Identity.SessionID, e.callTC.Identity.ProjectID)
	unjoined := jobs.UnjoinedFromCall(e.ctx) //nolint:contextcheck // Identifies the evaluated call's bare-wait set.
	if len(unjoined) == 0 || r.DerivedWatches == nil {
		return
	}
	handoffs := make([]ProgramBackgroundJob, 0, len(unjoined))
	for _, handle := range unjoined {
		h, ok, err := r.DerivedWatches.HandoffCommand(context.WithoutCancel(ctx), e.callTC, handle, 0)
		if err != nil || !ok {
			continue
		}
		wc := h.WaitCondition
		handoffs = append(handoffs, ProgramBackgroundJob{
			Handle:        handle,
			WatchHandle:   h.WatchHandle,
			WaitCondition: &wc,
			OutputCursor:  h.OutputCursor,
		})
	}
	if len(handoffs) > 0 {
		outcome.BackgroundJobs = handoffs
	}
}
