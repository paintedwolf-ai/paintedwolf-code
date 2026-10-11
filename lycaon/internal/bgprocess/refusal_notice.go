package bgprocess

import (
	"context"
	"time"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/hostcmd"
)

// refusalNoticeSettle gathers one burst of refusals into one notice.
const refusalNoticeSettle = 250 * time.Millisecond

// RefusalNotice reports unshown kernel refusals while the job continues running.
type RefusalNotice struct {
	Handle     string
	SessionID  string
	ProjectID  string
	OriginTool string
	ToolCallID string
	Mode       JobMode
	StartedAt  time.Time
	Stages     []hostcmd.StageResult
	// Observation carries every refusal so far.
	Observation confine.Observation
	// Unshown counts the refusals no earlier result showed.
	Unshown int
}

// RefusalPublisher receives refusal notices for visible running jobs.
type RefusalPublisher func(ctx context.Context, notice RefusalNotice)

func (r *Output) watchRefusals(ctx context.Context, proc *Process) {
	proc.facts.Action.OnRefusal(func() { r.refusalObserved(ctx, proc) })
	// Refusals can arrive before listener registration.
	if len(proc.facts.Refusals().Refusals) > 0 {
		r.refusalObserved(ctx, proc)
	}
}

func (r *Output) refusalObserved(ctx context.Context, proc *Process) {
	r.jobs.mu.Lock()
	defer r.jobs.mu.Unlock()
	if r.refused == nil || proc.refusalNotice != nil || !proc.running {
		return
	}
	proc.refusalNotice = time.AfterFunc(refusalNoticeSettle, func() { r.publishRefusals(ctx, proc) })
}

func (r *Output) publishRefusals(ctx context.Context, proc *Process) {
	r.jobs.mu.Lock()
	proc.refusalNotice = nil
	r.jobs.mu.Unlock()
	r.publishRefusalSnapshot(ctx, proc, proc.facts.Refusals())
}

func (r *Output) publishRefusalSnapshot(ctx context.Context, proc *Process, refusals confine.SandboxRefusals) {
	r.jobs.mu.Lock()
	// Silent foreground jobs report refusals in their tool result.
	if r.refused == nil || proc.silent || proc.discarded || !proc.running || len(refusals.Refusals) <= proc.refusalsShown {
		r.jobs.mu.Unlock()
		return
	}
	notice := RefusalNotice{
		Handle: proc.Handle, SessionID: proc.SessionID, ProjectID: proc.ProjectID,
		OriginTool: proc.originTool, ToolCallID: proc.toolCallID, Mode: proc.mode,
		StartedAt: proc.startedAt, Stages: append([]hostcmd.StageResult(nil), proc.Stages...),
		Unshown: len(refusals.Refusals) - proc.refusalsShown,
	}
	proc.refusalsShown = len(refusals.Refusals)
	boundary, facts, publish := proc.boundary, proc.facts, r.refused
	r.jobs.mu.Unlock()
	notice.Observation = confine.StampRefusal(notice.OriginTool, notice.SessionID, boundary, confine.RefusalContext{
		MediatedNetwork:        facts.MediatedNetwork(),
		RemotePackageExecution: facts.Report.RemotePackageExecution,
		Running:                true,
		Refusals:               refusals,
	}).Observation
	publish(ctx, notice)
}

// NoteRefusalsShown advances the notice watermark after a tool result.
func (r *Output) NoteRefusalsShown(sessionID, handle string, shown int) {
	proc, err := r.jobs.lookup(sessionID, handle)
	if err != nil || proc == nil {
		return
	}
	r.jobs.mu.Lock()
	proc.refusalsShown = max(proc.refusalsShown, shown)
	r.jobs.mu.Unlock()
}
