package coordgrounding

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/grounding"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/workercompletion"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

const ambientGroundingStatePrefix = "ambient:"

type WorkerQueue interface {
	ListBySession(ctx context.Context, projectDir, sessionID string, status ...api.WorkerStatus) ([]api.WorkerTask, error)
}

// AmbientGroundingInput carries per-turn coordinator grounding inputs.
type AmbientGroundingInput struct {
	SessionID              string
	ProjectID              string
	ProjectDir             string
	Jobs                   []api.WorkerTask
	SummaryTags            []grounding.WorkerSummaryTag
	LastTurnTools          []string
	LastAuditUngrounded    bool
	LastAuditHostAssembled bool
	LastAuditCode          string
	UngroundedState        grounding.UngroundedCounter
}

// AmbientGroundingGate evaluates coordinator turns against worker jobs and citation audits.
type AmbientGroundingGate interface {
	CheckTurn(ctx context.Context, in AmbientGroundingInput) grounding.GroundingVerdict
}

// SimpleAmbientGroundingGate correlates worker jobs, summary tags, and citation audits.
type SimpleAmbientGroundingGate struct {
	cfg grounding.GroundingConfig
}

func NewSimpleAmbientGroundingGate(cfg grounding.GroundingConfig) *SimpleAmbientGroundingGate {
	if cfg.PostTurn.Mode == "" {
		cfg = grounding.DefaultGroundingConfig()
	}
	return &SimpleAmbientGroundingGate{cfg: cfg}
}

// CheckTurn evaluates coordinator sessions for post-turn grounding gaps.
func (g *SimpleAmbientGroundingGate) CheckTurn(_ context.Context, in AmbientGroundingInput) grounding.GroundingVerdict {
	mode := g.cfg.AmbientPostTurnMode()
	if mode == "off" || in.LastAuditHostAssembled {
		return grounding.GroundingVerdict{OK: true}
	}
	if len(in.Jobs) == 0 {
		if in.LastAuditUngrounded {
			code := strings.TrimSpace(in.LastAuditCode)
			if code == "" {
				code = grounding.AmbientUngroundedCompletionCode
			}
			return grounding.GroundingVerdict{
				OK:     false,
				Code:   code,
				Reason: "coordinator citations did not trace to tool evidence",
			}
		}
		return grounding.GroundingVerdict{OK: true}
	}
	if containsTool(in.LastTurnTools, "task") ||
		containsTool(in.LastTurnTools, "instruct_worker") ||
		containsTool(in.LastTurnTools, "wait") {
		return grounding.GroundingVerdict{OK: true}
	}
	if ambientLedgerSatisfied(in.Jobs, in.SummaryTags) {
		return grounding.GroundingVerdict{OK: true}
	}
	return grounding.GroundingVerdict{
		OK:     false,
		Code:   grounding.AmbientUngroundedCompletionCode,
		Reason: "worker job ledger not satisfied on parent session",
	}
}

func containsTool(names []string, target string) bool {
	for _, n := range names {
		if n == target {
			return true
		}
	}
	return false
}

func ambientLedgerSatisfied(jobs []api.WorkerTask, tags []grounding.WorkerSummaryTag) bool {
	for _, j := range jobs {
		switch j.Status {
		case api.WorkerStatusPending, api.WorkerStatusRunning, api.WorkerStatusHeld:
			return true
		case api.WorkerStatusWaiting, api.WorkerStatusComplete, api.WorkerStatusFailed, api.WorkerStatusCanceled:
		}
	}
	for _, j := range jobs {
		if j.Status == api.WorkerStatusComplete && !hasSummaryForJob(tags, j.ID) {
			return false
		}
	}
	return true
}

func hasSummaryForJob(tags []grounding.WorkerSummaryTag, jobID string) bool {
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return false
	}
	for _, t := range tags {
		if t.JobID == jobID {
			return true
		}
	}
	return false
}

func ambientStateKey(sessionID string) string {
	return ambientGroundingStatePrefix + strings.TrimSpace(sessionID)
}

// AmbientGroundingCoordinator wires post-turn grounding and escalation into session prompts.
type AmbientGroundingCoordinator struct {
	Queue    WorkerQueue
	Gate     AmbientGroundingGate
	Config   grounding.GroundingConfig
	State    *grounding.StateStore
	Sessions *session.Host
	Events   *events.Publisher
	Pipeline *oar.GuardPipeline
	Names    workercompletion.WorkerNames
}

var _ session.GroundingHook = (*AmbientGroundingCoordinator)(nil)

func NewAmbientGroundingCoordinator(queue WorkerQueue, gate AmbientGroundingGate, cfg grounding.GroundingConfig, state *grounding.StateStore, sessions *session.Host) *AmbientGroundingCoordinator {
	if gate == nil {
		gate = NewSimpleAmbientGroundingGate(cfg)
	}
	if state == nil {
		state = grounding.NewStateStore()
	}
	return &AmbientGroundingCoordinator{
		Queue:    queue,
		Gate:     gate,
		Config:   cfg,
		State:    state,
		Sessions: sessions,
	}
}

// IsEscalated reports circuit-breaker escalation for the session.
func (g *AmbientGroundingCoordinator) IsEscalated(sessionID string) bool {
	if g == nil || g.State == nil {
		return false
	}
	return g.State.IsEscalated(sessionID) || g.State.IsEscalated(ambientStateKey(sessionID))
}

// Reset clears session and ambient grounding counters.
func (g *AmbientGroundingCoordinator) Reset(sessionID string) {
	if g == nil || g.State == nil {
		return
	}
	g.State.Reset(sessionID)
	g.State.Reset(ambientStateKey(sessionID))
	if g.Pipeline != nil && g.Pipeline.Counters() != nil {
		store := g.Pipeline.Counters()
		store.Reset(sessionID, grounding.CoordinatorUngroundedClaimCode, oar.CounterBreaker)
		store.Reset(sessionID, grounding.AmbientUngroundedCompletionCode, oar.CounterBreaker)
		store.Reset(sessionID, grounding.AmbientGroundingEscalatedCode, oar.CounterBreaker)
	}
}

// AfterPrompt runs post-turn escalation and ambient grounding checks.
func (g *AmbientGroundingCoordinator) AfterPrompt(ctx context.Context, sessionID string, lastTools []string) error {
	if g == nil || g.Gate == nil {
		return nil
	}
	if containsTool(lastTools, "task") || containsTool(lastTools, "instruct_worker") {
		g.Reset(sessionID)
	}
	in, err := g.buildInput(ctx, sessionID, lastTools)
	if err != nil {
		return err
	}
	if err := g.checkCloseoutEscalation(ctx, sessionID, in.ProjectID); err != nil {
		return err
	}
	verdict := g.Gate.CheckTurn(ctx, in)
	if g.Pipeline == nil || !g.Pipeline.AnchorEnforced(oar.AnchorCoordinatorPostTurn) {
		return nil
	}
	return g.applyOARAmbientGrounding(ctx, sessionID, ambientStateKey(sessionID), in, verdict)
}

func (g *AmbientGroundingCoordinator) checkCloseoutEscalation(ctx context.Context, sessionID, projectID string) error {
	if g.Config.PostTurn.Mode == "off" || g.Config.CircuitBreaker.EscalateMode != "block" || g.State == nil {
		return nil
	}
	state := g.State.Get(sessionID)
	if g.Pipeline != nil && g.Pipeline.Counters() != nil {
		if n := int(g.Pipeline.Counters().Get(sessionID, grounding.CoordinatorUngroundedClaimCode, oar.CounterBreaker)); n > state.ConsecutiveWarnings {
			state.ConsecutiveWarnings = n
			state.TotalWarnings = n
		}
		if grounding.Flagged(state, g.Config) {
			state.Escalated = true
		}
		g.State.Set(sessionID, state)
	}
	if !state.Escalated {
		return nil
	}
	if g.Sessions != nil {
		g.Sessions.Runner.Closeouts.RecordGroundingFriction(ctx, sessionID)
	}
	verdict := grounding.GroundingVerdict{
		OK:     false,
		Code:   grounding.CoordinatorEscalatedCode,
		Reason: "repeated ungrounded completion claims",
	}
	if g.Pipeline != nil && g.Pipeline.AnchorEnforced(oar.AnchorCoordinatorPostTurn) {
		gc := oar.NewGuardContext()
		gc.Session.SessionID = sessionID
		gc.Session.Role = "coordinator"
		tools.RegisterRecoveryFacts(ctx, gc)
		grounding.ObserveVerdict(gc, verdict)
		if _, err := g.Pipeline.EvaluateBlock(ctx, oar.AnchorCoordinatorPostTurn, gc); err != nil {
			return err
		}
	}
	if g.Events != nil && strings.TrimSpace(projectID) != "" {
		g.Events.PublishGrounding(ctx, projectID, sessionID, verdict.Code, "", "", true)
	}
	return guidance.ErrGroundingEscalated
}

func (g *AmbientGroundingCoordinator) applyOARAmbientGrounding(ctx context.Context, sessionID, key string, in AmbientGroundingInput, verdict grounding.GroundingVerdict) error {
	if verdict.OK {
		return nil
	}
	gc := oar.NewGuardContext()
	gc.Session.SessionID = sessionID
	gc.Session.Role = "coordinator"
	tools.RegisterRecoveryFacts(ctx, gc)
	grounding.ObserveVerdict(gc, verdict)
	gc.Grounding.GroundingEscalated = grounding.Flagged(g.State.Get(key), g.Config)
	missing, workers := 0, []string{}
	for _, job := range in.Jobs {
		if job.Status == api.WorkerStatusComplete && !hasSummaryForJob(in.SummaryTags, job.ID) {
			missing++
			if name := g.Names.Name(ctx, job.ID); name != "" {
				workers = append(workers, name)
			}
		}
	}
	gc.PutRejectData(grounding.AmbientUngroundedCompletionCode, map[string]any{"missing_summary_count": missing, "missing_summary_workers": workers})
	if _, err := g.Pipeline.EvaluateBlock(ctx, oar.AnchorCoordinatorPostTurn, gc); err != nil {
		return err
	}
	state := g.State.Get(key)
	if store := g.Pipeline.Counters(); store != nil {
		n := store.Get(sessionID, grounding.AmbientUngroundedCompletionCode, oar.CounterBreaker)
		state.ConsecutiveWarnings = int(n)
		state.TotalWarnings = int(n)
		g.State.Set(key, state)
	}
	flagged := grounding.Flagged(state, g.Config)
	if g.Events != nil {
		code := verdict.Code
		if flagged {
			code = grounding.AmbientGroundingEscalatedCode
		}
		g.Events.PublishGrounding(ctx, in.ProjectID, sessionID, code, "", "", flagged)
	}
	return nil
}

func lastAssistantAudit(msgs []api.Message) *api.CitationGrounding {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == api.MessageRoleAssistant && !api.IsAgentNoteMessage(msgs[i]) {
			return msgs[i].Grounding
		}
	}
	return nil
}

func (g *AmbientGroundingCoordinator) buildInput(ctx context.Context, sessionID string, lastTools []string) (AmbientGroundingInput, error) {
	in := AmbientGroundingInput{
		SessionID:     sessionID,
		LastTurnTools: append([]string(nil), lastTools...),
	}
	if g.Sessions == nil {
		return in, nil
	}
	sess, err := g.Sessions.Chats.Get(ctx, sessionID)
	if err != nil {
		return AmbientGroundingInput{}, err
	}
	if sess != nil {
		in.ProjectID = sess.ProjectID
		in.ProjectDir = sess.WorkspacePath
	}
	if g.Queue != nil && sess != nil {
		jobs, _ := g.Queue.ListBySession(ctx, sess.ProjectID, sessionID)
		in.Jobs = append([]api.WorkerTask(nil), jobs...)
	}
	msgs, _ := g.Sessions.Runner.Transcript.GetMessages(ctx, sessionID)
	in.SummaryTags = summaryTagsFromMessages(msgs)
	if audit := lastAssistantAudit(msgs); audit != nil {
		in.LastAuditHostAssembled = audit.HostAssembled
		if !audit.Traced {
			in.LastAuditUngrounded = true
			in.LastAuditCode = strings.TrimSpace(audit.HintCode)
		}
	}
	in.UngroundedState = g.State.Get(ambientStateKey(sessionID))
	return in, nil
}

func summaryTagsFromMessages(msgs []api.Message) []grounding.WorkerSummaryTag {
	var out []grounding.WorkerSummaryTag
	for _, msg := range msgs {
		if msg.WorkerSummary == nil {
			continue
		}
		out = append(out, grounding.WorkerSummaryTag{
			DelegationID: msg.WorkerSummary.DelegationID,
			LegID:        msg.WorkerSummary.LegID,
			JobID:        msg.WorkerSummary.WorkerID,
			MessageID:    msg.ID,
			TS:           msg.CreatedAt,
		})
	}
	return out
}
