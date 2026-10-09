package promptloop

import (
	"context"

	"github.com/lycaon/lycaon/internal/agentpresence"
	"github.com/lycaon/lycaon/pkg/api"
)

// callPresence binds one tool call to the agent presence projection.
type callPresence struct {
	ctx     context.Context
	tracker *agentpresence.Tracker
	call    agentpresence.Call
}

func (p callPresence) Target(target agentpresence.Target, kind api.AgentActivityKind) {
	p.tracker.CallStarted(p.ctx, p.call, target, kind)
}

func (p callPresence) Intents(intents []agentpresence.Intent) {
	p.tracker.IntentsResolved(p.ctx, p.call, intents)
}

func (p callPresence) AwaitingApproval(checkpointID string) {
	p.tracker.IntentsAwaitingApproval(p.ctx, p.call, checkpointID)
}

func (p callPresence) Approved() {
	p.tracker.IntentsApproved(p.ctx, p.call)
}

func (p callPresence) Landed(documents []agentpresence.Document) {
	p.tracker.IntentsLanded(p.ctx, p.call, documents)
}

func (p callPresence) Reserved(targets []agentpresence.Target) {
	p.tracker.WorkerReserved(p.ctx, p.call.SessionID, targets)
}

func (p callPresence) Released(targets []agentpresence.Target) {
	p.tracker.WorkerReleased(p.ctx, p.call.SessionID, targets)
}

// recordReturnedText reports the text a successful call returned to the model.
func (l *toolInvocations) recordReturnedText(ctx context.Context, sessionID string, tc api.ToolCall, run toolInvocation) {
	if l.Deps.AgentPresence == nil || !run.succeeded() || len(run.captures.sourceReads) == 0 {
		return
	}
	l.Deps.AgentPresence.ReadsReturned(ctx, agentpresence.Call{SessionID: sessionID, ToolCallID: tc.ID, Tool: tc.Name}, run.captures.sourceReads)
}
