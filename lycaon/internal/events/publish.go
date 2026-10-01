package events

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/pkg/api"
)

// logPublishFailure records delivery errors with their publishing method and topic.
func logPublishFailure(ctx context.Context, method string, topic api.EventTopic, err error) {
	if err == nil {
		return
	}
	slog.WarnContext(ctx, "event publish failed", "method", method, "topic", topic, "err", err)
}

// BoardViewSource builds slim board views for SSE board topic events.
type BoardViewSource interface {
	BuildView(ctx context.Context, projectID, workspacePath, sessionID string, level api.BoardDetailLevel) (api.BoardView, error)
}

// SessionUIProvider computes host-managed session chrome for SSE and GET enrichment.
type SessionUIProvider interface {
	ComputeSessionUI(ctx context.Context, sessionID string) (*api.SessionUiState, error)
}

// UntrustedContentSource reports the session-level untrusted-content fact for SSE.
type UntrustedContentSource interface {
	SessionUntrustedContent(sessionID string) bool
}

// UserTurnSource reports the newest host user-turn ordinal.
type UserTurnSource interface {
	UserTurnOrdinal(ctx context.Context, sessionID string) (int, error)
}

// SessionStateSource reads a session's status and prompt_pending.
type SessionStateSource interface {
	SessionState(ctx context.Context, sessionID string) (api.SessionStatus, bool, error)
}

// ActivityObserver receives host activity edges.
type ActivityObserver interface {
	ObserveActivity(api.ActivityEvent)
}

// TurnClockObserver receives session-tree clock edges.
type TurnClockObserver interface {
	ObserveTurnClock(api.TurnClock)
}

// SessionObserver receives session lifecycle transitions as they publish.
type SessionObserver interface {
	ObserveSession(context.Context, api.SessionEvent)
}

// SessionRoots resolves a session's active workspace root.
type SessionRoots interface {
	ActiveRootPath(ctx context.Context, sessionID string) (string, bool)
}

// FuncSessionRoots adapts a function to SessionRoots.
type FuncSessionRoots func(ctx context.Context, sessionID string) (string, bool)

// ActiveRootPath implements SessionRoots.
func (f FuncSessionRoots) ActiveRootPath(ctx context.Context, sessionID string) (string, bool) {
	if f == nil {
		return "", false
	}
	return f(ctx, sessionID)
}

// Publisher wires EventHub publish helpers with canonical project_id resolution.
type Publisher struct {
	Hub               EventHub
	Lookup            ProjectLookup
	Board             BoardViewSource
	SessionUI         SessionUIProvider
	Untrusted         UntrustedContentSource
	UserTurns         UserTurnSource
	SessionState      SessionStateSource
	SessionRoots      SessionRoots
	SessionLister     SessionLister
	SessionProject    func(context.Context, string) (string, bool)
	Attention         AttentionViewSource
	ActivityObserver  ActivityObserver
	TurnClockObserver TurnClockObserver
	SessionObserver   SessionObserver
	closed            atomic.Bool
	activitiesMu      sync.Mutex
	activities        map[string]map[string]api.ActivityEvent

	messagePatchesMu sync.Mutex
	messagePatches   *messagePatchCoalescer

	attentionMu         sync.Mutex
	attentionTimer      *time.Timer
	attentionWG         sync.WaitGroup
	attentionDeliveryMu sync.Mutex
	// attentionMu guards build order and the latest accepted revision.
	attentionRevision  uint64
	attentionPublished uint64

	// boardRevision orders concurrent board builds by call order.
	boardRevision atomic.Uint64

	// sessionRevision orders direct and outbox events against the same counter.
	sessionRevision atomic.Uint64
}

// NextSessionRevision mints the next session-event revision; session/store's
// outbox path stamps PublishKey.EntityRevision from the same counter.
func (p *Publisher) NextSessionRevision() uint64 {
	if p == nil {
		return 0
	}
	return p.sessionRevision.Add(1)
}

func (p *Publisher) sessionKey(ctx context.Context, sessionID string) PublishKey {
	projectID := ""
	if p != nil && p.SessionProject != nil {
		projectID, _ = p.SessionProject(ctx, strings.TrimSpace(sessionID))
	}
	return PublishKeyFor(ctx, p.Lookup, projectID, sessionID)
}

// sessionEventFields stamps the revision before reads so late results retain
// their build order. An event without a status takes the one read here.
func (p *Publisher) sessionEventFields(ctx context.Context, ev *api.SessionEvent) (revision uint64) {
	revision = p.NextSessionRevision()
	if p.UserTurns != nil {
		if turn, err := p.UserTurns.UserTurnOrdinal(ctx, ev.ID); err == nil {
			ev.CurrentTurn = turn
		}
	}
	if p.SessionState != nil {
		if status, pending, err := p.SessionState.SessionState(ctx, ev.ID); err == nil {
			ev.PromptPending = pending
			if ev.Status == "" {
				ev.Status = status
			}
		}
	}
	if p.SessionUI != nil {
		if computed, err := p.SessionUI.ComputeSessionUI(ctx, ev.ID); err == nil {
			ev.UI = computed
		}
	}
	return revision
}

// PublishSession emits a session event. A caller that did not just commit the
// status under the session lock passes an empty status, read after the revision.
func (p *Publisher) PublishSession(ctx context.Context, projectIDOrDir, sessionID string, status api.SessionStatus, lastMessage string) {
	p.publishSession(ctx, projectIDOrDir, api.SessionEvent{ID: sessionID, Status: status, LastMessage: lastMessage})
}

// PublishSessionIdle emits a typed idle transition.
func (p *Publisher) PublishSessionIdle(
	ctx context.Context,
	projectIDOrDir, sessionID, lastMessage string,
	disposition api.SessionIdleDisposition,
) {
	p.publishSession(ctx, projectIDOrDir, api.SessionEvent{
		ID: sessionID, Status: api.SessionStatusIdle, LastMessage: lastMessage, IdleDisposition: disposition,
	})
}

// publishSession completes and publishes ev, which names the session and its edge.
func (p *Publisher) publishSession(ctx context.Context, projectIDOrDir string, ev api.SessionEvent) {
	if p == nil || p.Hub == nil {
		return
	}
	sessionID := ev.ID
	if ev.Status != api.SessionStatusBusy {
		if c := p.messagePatchCoalescer(); c != nil {
			c.flushSession(ctx, sessionID)
		}
	}
	key := PublishKeyFor(ctx, p.Lookup, projectIDOrDir, sessionID)
	ev.ProjectID = key.Project
	ev.Action = api.SessionEventActionUpdated
	if p.Untrusted != nil {
		ev.UntrustedContent = p.Untrusted.SessionUntrustedContent(sessionID)
	}
	key.EntityRevision = p.sessionEventFields(ctx, &ev)
	if p.SessionObserver != nil {
		p.SessionObserver.ObserveSession(ctx, ev)
	}
	logPublishFailure(ctx, "publishSession", api.EventTopicSession, p.Hub.Publish(ctx, api.EventTopicSession, key, ev))
	p.PublishAttention(ctx)
}

// PublishSessionHostError emits a structured host failure with the session's current state.
func (p *Publisher) PublishSessionHostError(ctx context.Context, projectIDOrDir, sessionID string, hostErr api.SessionHostError) {
	p.publishSession(ctx, projectIDOrDir, api.SessionEvent{ID: sessionID, HostError: &hostErr})
}

// SessionLister names the sessions of a project that present its board.
type SessionLister interface {
	ProjectSessionIDs(ctx context.Context, projectID string) ([]string, error)
}

// FuncSessionLister adapts a function to SessionLister.
type FuncSessionLister func(ctx context.Context, projectID string) ([]string, error)

// ProjectSessionIDs implements SessionLister.
func (f FuncSessionLister) ProjectSessionIDs(ctx context.Context, projectID string) ([]string, error) {
	return f(ctx, projectID)
}

// PublishBoardForRoot refreshes every session board for the project at projectDir.
func (p *Publisher) PublishBoardForRoot(ctx context.Context, projectDir string) {
	if p == nil || p.Hub == nil || p.Board == nil || p.SessionLister == nil {
		return
	}
	key := PublishKeyFor(ctx, p.Lookup, projectDir, "")
	if strings.TrimSpace(key.Project) == "" {
		return
	}
	sessionIDs, err := p.SessionLister.ProjectSessionIDs(ctx, key.Project)
	if err != nil {
		slog.WarnContext(ctx, "board republish: list sessions", "project_id", key.Project, "err", err)
		return
	}
	for _, sessionID := range sessionIDs {
		p.PublishBoard(ctx, key.Project, sessionID)
	}
}

// PublishBoard stamps revisions before building and coalesces snapshots per session.
func (p *Publisher) PublishBoard(ctx context.Context, projectIDOrDir, sessionID string) {
	if p == nil || p.Hub == nil || p.Board == nil {
		return
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return
	}
	revision := p.boardRevision.Add(1)
	key := PublishKeyFor(ctx, p.Lookup, projectIDOrDir, "")
	key.Facet = sessionID
	key.EntityRevision = revision
	workspacePath := ""
	if p.SessionRoots != nil {
		if path, ok := p.SessionRoots.ActiveRootPath(ctx, sessionID); ok {
			workspacePath = strings.TrimSpace(path)
		}
	}
	if workspacePath == "" {
		workspacePath = resolveProjectDir(ctx, p.Lookup, projectIDOrDir)
	}
	view, err := p.Board.BuildView(ctx, key.Project, workspacePath, sessionID, api.BoardDetailLevelCompact)
	if err != nil {
		return
	}
	logPublishFailure(ctx, "PublishBoard", api.EventTopicBoard, p.Hub.Publish(ctx, api.EventTopicBoard, key, api.BoardEvent{
		ProjectID:   key.Project,
		SessionID:   sessionID,
		Snapshot:    view,
		DetailLevel: view.DetailLevel,
	}))
}

// PublishLLM emits a session-scoped provider-call lifecycle event.
func (p *Publisher) PublishLLM(ctx context.Context, projectIDOrDir, sessionID string, ev api.LLMCallEvent) {
	if p == nil || p.Hub == nil {
		return
	}
	if ev.CallID == "" {
		ev.CallID = uuid.NewString()
	}
	ev.SessionID = sessionID
	key := PublishKeyFor(ctx, p.Lookup, projectIDOrDir, sessionID)
	logPublishFailure(ctx, "PublishLLM", api.EventTopicLLM, p.Hub.Publish(ctx, api.EventTopicLLM, key, ev))
}

// PublishActivity emits one lifecycle edge for session-scoped host work.
func (p *Publisher) PublishActivity(ctx context.Context, projectIDOrDir, sessionID string, ev api.ActivityEvent) {
	if p == nil {
		return
	}
	ev.SessionID = strings.TrimSpace(sessionID)
	p.observeActivity(ev)
	if p.ActivityObserver != nil {
		p.ActivityObserver.ObserveActivity(ev)
	}
	if p.Hub == nil {
		return
	}
	key := PublishKeyFor(ctx, p.Lookup, projectIDOrDir, ev.SessionID)
	logPublishFailure(ctx, "PublishActivity", api.EventTopicActivity, p.Hub.Publish(ctx, api.EventTopicActivity, key, ev))
}

// PublishOAR emits an on_fire event.
func (p *Publisher) PublishOAR(ctx context.Context, sessionID string, ev api.OAROnFireEvent) error {
	if p == nil || p.Hub == nil {
		return fmt.Errorf("OAR event publisher is unavailable")
	}
	ev.SessionID = strings.TrimSpace(sessionID)
	key := p.sessionKey(ctx, ev.SessionID)
	return p.Hub.Publish(ctx, api.EventTopicOAR, key, ev)
}

// PublishCost emits a cost rollup event.
func (p *Publisher) PublishCost(ctx context.Context, projectIDOrDir, sessionID string, ev api.CostEvent) {
	if p == nil || p.Hub == nil {
		return
	}
	key := PublishKeyFor(ctx, p.Lookup, projectIDOrDir, sessionID)
	logPublishFailure(ctx, "PublishCost", api.EventTopicCost, p.Hub.Publish(ctx, api.EventTopicCost, key, ev))
}

// stampRunRevision scopes revision comparisons to one run through Facet.
// Events without runs use immediate delivery and have no revision to compare.
func stampRunRevision(key PublishKey, ev api.WorkflowEvent) PublishKey {
	if ev.Run == nil || ev.Run.Revision <= 0 {
		return key
	}
	key.Facet = ev.Run.ID
	key.EntityRevision = uint64(ev.Run.Revision)
	return key
}

// PublishWorkflowPersisted emits workflow.persisted after project overlay write.
func (p *Publisher) PublishWorkflowPersisted(ctx context.Context, projectIDOrDir, sessionID string, ev api.WorkflowEvent) {
	if p == nil || p.Hub == nil {
		return
	}
	key := stampRunRevision(PublishKeyFor(ctx, p.Lookup, projectIDOrDir, sessionID), ev)
	logPublishFailure(ctx, "PublishWorkflowPersisted", api.EventTopicWorkflow, p.Hub.Publish(ctx, api.EventTopicWorkflow, key, ev))
}

// PublishWorkflow emits workflow run lifecycle events and refreshes the board snapshot.
func (p *Publisher) PublishWorkflow(ctx context.Context, projectIDOrDir, sessionID string, ev api.WorkflowEvent) {
	if p == nil || p.Hub == nil {
		return
	}
	key := stampRunRevision(PublishKeyFor(ctx, p.Lookup, projectIDOrDir, sessionID), ev)
	logPublishFailure(ctx, "PublishWorkflow", api.EventTopicWorkflow, p.Hub.Publish(ctx, api.EventTopicWorkflow, key, ev))
	p.PublishBoard(ctx, projectIDOrDir, sessionID)
	// Run lifecycle opens and closes ask latches.
	p.PublishAttention(ctx)
}

// RefreshWorkflowDerived recomputes workflow projections.
func (p *Publisher) RefreshWorkflowDerived(ctx context.Context, projectIDOrDir, sessionID string) {
	if p == nil {
		return
	}
	p.PublishBoard(ctx, projectIDOrDir, sessionID)
	p.PublishAttention(ctx)
}

// PublishGrounding emits delegation grounding warn/escalate events.
func (p *Publisher) PublishGrounding(ctx context.Context, projectIDOrDir, sessionID, code, legID, delegationID string, escalated bool) {
	if p == nil || p.Hub == nil {
		return
	}
	key := PublishKeyFor(ctx, p.Lookup, projectIDOrDir, sessionID)
	logPublishFailure(ctx, "PublishGrounding", api.EventTopicGrounding, p.Hub.Publish(ctx, api.EventTopicGrounding, key, api.GroundingEvent{
		SessionID:    sessionID,
		Code:         code,
		LegID:        legID,
		DelegationID: delegationID,
		Escalated:    escalated,
	}))
}

// PublishFindings emits findings.updated (session-keyed) after a record_finding append.
func (p *Publisher) PublishFindings(ctx context.Context, sessionID string, revision uint64) {
	if p == nil || p.Hub == nil {
		return
	}
	key := p.sessionKey(ctx, sessionID)
	key.EntityRevision = revision
	logPublishFailure(ctx, "PublishFindings", api.EventTopicFindings, p.Hub.Publish(ctx, api.EventTopicFindings, key, api.FindingsEvent{
		Revision: revision,
	}))
}

// PublishProgress emits progress.updated (session-keyed) after an update_progress write.
func (p *Publisher) PublishProgress(ctx context.Context, sessionID string, revision uint64) {
	if p == nil || p.Hub == nil {
		return
	}
	key := p.sessionKey(ctx, sessionID)
	key.EntityRevision = revision
	logPublishFailure(ctx, "PublishProgress", api.EventTopicProgress, p.Hub.Publish(ctx, api.EventTopicProgress, key, api.ProgressEvent{
		Revision: revision,
	}))
}

// PublishTurnClock emits clock transitions; clients advance time between events.
func (p *Publisher) PublishTurnClock(ctx context.Context, ev api.TurnClock) {
	if p == nil {
		return
	}
	if p.TurnClockObserver != nil {
		p.TurnClockObserver.ObserveTurnClock(ev)
	}
	if p.Hub == nil {
		return
	}
	key := p.sessionKey(ctx, ev.SessionID)
	logPublishFailure(ctx, "PublishTurnClock", api.EventTopicTurnClock, p.Hub.Publish(ctx, api.EventTopicTurnClock, key, ev))
}

// PublishTurnLoad emits one decision-engine receipt as it lands.
func (p *Publisher) PublishTurnLoad(ctx context.Context, ev api.TurnLoad) {
	if p == nil || p.Hub == nil {
		return
	}
	key := p.sessionKey(ctx, ev.SessionID)
	logPublishFailure(ctx, "PublishTurnLoad", api.EventTopicTurnLoad, p.Hub.Publish(ctx, api.EventTopicTurnLoad, key, ev))
}

// PublishQueue emits queue.updated (session-keyed) after a draft mutation.
func (p *Publisher) PublishQueue(ctx context.Context, sessionID string, revision uint64) {
	if p == nil || p.Hub == nil {
		return
	}
	key := p.sessionKey(ctx, sessionID)
	key.EntityRevision = revision
	logPublishFailure(ctx, "PublishQueue", api.EventTopicQueue, p.Hub.Publish(ctx, api.EventTopicQueue, key, api.QueueEvent{
		Revision: revision,
	}))
}

// PublishProcess emits incremental background process output for Den.
func (p *Publisher) PublishProcess(ctx context.Context, projectIDOrDir, sessionID string, ev api.BackgroundProcessEvent) {
	if p == nil || p.Hub == nil {
		return
	}
	ev.SessionID = strings.TrimSpace(sessionID)
	key := PublishKeyFor(ctx, p.Lookup, projectIDOrDir, sessionID)
	logPublishFailure(ctx, "PublishProcess", api.EventTopicProcess, p.Hub.Publish(ctx, api.EventTopicProcess, key, ev))
}

// PublishPreview emits a live page preview frame or lifecycle event for Den.
func (p *Publisher) PublishPreview(ctx context.Context, projectIDOrDir, sessionID string, ev api.PreviewEvent) {
	if p == nil || p.Hub == nil {
		return
	}
	ev.SessionID = strings.TrimSpace(sessionID)
	key := PublishKeyFor(ctx, p.Lookup, projectIDOrDir, sessionID)
	logPublishFailure(ctx, "PublishPreview", api.EventTopicPreview, p.Hub.Publish(ctx, api.EventTopicPreview, key, ev))
}

// PublishCheckpoint emits human checkpoint pending/resolved events (immediate delivery).
func (p *Publisher) PublishCheckpoint(ctx context.Context, projectIDOrDir, sessionID string, ev api.CheckpointEvent) {
	if p == nil || p.Hub == nil {
		return
	}
	key := PublishKeyFor(ctx, p.Lookup, projectIDOrDir, sessionID)
	logPublishFailure(ctx, "PublishCheckpoint", api.EventTopicCheckpoint, p.Hub.Publish(ctx, api.EventTopicCheckpoint, key, ev))
	// Opening and resolving checkpoints both change the attention view.
	p.PublishAttention(ctx)
}

// PublishGatePending emits workflow gate failure with scan guidance.
func (p *Publisher) PublishGatePending(ctx context.Context, projectIDOrDir, sessionID string, ev api.WorkflowEvent) {
	if p == nil || p.Hub == nil {
		return
	}
	key := stampRunRevision(PublishKeyFor(ctx, p.Lookup, projectIDOrDir, sessionID), ev)
	logPublishFailure(ctx, "PublishGatePending", api.EventTopicWorkflow, p.Hub.Publish(ctx, api.EventTopicWorkflow, key, ev))
	p.PublishAttention(ctx)
}

// PublishCLIOpen uses an empty project key to reach windows displaying any project.
func (p *Publisher) PublishCLIOpen(ctx context.Context, ev api.CLIOpenEvent) {
	if p == nil || p.Hub == nil {
		return
	}
	logPublishFailure(ctx, "PublishCLIOpen", api.EventTopicCLIOpen, p.Hub.Publish(ctx, api.EventTopicCLIOpen, PublishKey{}, ev))
}

// PublishPreflight broadcasts readiness changes across project filters.
func (p *Publisher) PublishPreflight(ctx context.Context, probeID string) {
	if p == nil || p.Hub == nil {
		return
	}
	logPublishFailure(ctx, "PublishPreflight", api.EventTopicPreflight, p.Hub.Publish(ctx, api.EventTopicPreflight, PublishKey{}, api.PreflightEvent{ProbeID: probeID}))
}

// PublishAgentPresence emits one chat's complete agent presence; the revision
// lets clients drop an older value that arrives late.
func (p *Publisher) PublishAgentPresence(ctx context.Context, ev api.AgentPresenceEvent) {
	if p == nil || p.Hub == nil || strings.TrimSpace(ev.ProjectID) == "" {
		return
	}
	key := PublishKey{Project: ev.ProjectID, Facet: ev.SessionID, EntityRevision: uint64(max(ev.Revision, 0))}
	logPublishFailure(ctx, "PublishAgentPresence", api.EventTopicAgentPresence, p.Hub.Publish(ctx, api.EventTopicAgentPresence, key, ev))
}
