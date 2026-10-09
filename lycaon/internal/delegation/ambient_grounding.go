package delegation

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/grounding"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/pkg/api"
)

const (
	ambientGroundingStatePrefix     = "ambient:"
	ambientUngroundedCompletionCode = "AMBIENT_UNGROUNDED_COMPLETION"
	ambientGroundingEscalatedCode   = "AMBIENT_GROUNDING_ESCALATED"
)

type ambientWorkerQueue interface {
	ListBySession(ctx context.Context, projectDir, sessionID string, status ...api.WorkerStatus) ([]api.WorkerTask, error)
}

// AmbientGroundingInput is the per-turn input for grounding a default Build coordinator
// session (no delegation): worker queue + summary tags, or the last prose turn's citation audit.
type AmbientGroundingInput struct {
	SessionID     string
	ProjectID     string
	ProjectDir    string
	Jobs          []api.WorkerTask
	SummaryTags   []WorkerSummaryTag
	LastTurnTools []string
	// LastAuditUngrounded is set when the coordinator's last prose turn carried a
	// citation-grounding audit whose typed citations did not trace to the evidence
	// ledger. The no-worker coordinator is grounded on this flag, never on re-scanned prose.
	LastAuditUngrounded bool
	LastAuditCode       string
	UngroundedState     grounding.UngroundedCounter
}

// AmbientGroundingGate grounds default-Build coordinator turns: the worker queue + worker_summary
// tags when workers are present, or the coordinator's own citation audit when they are not.
type AmbientGroundingGate interface {
	CheckTurn(ctx context.Context, in AmbientGroundingInput) GroundingVerdict
}

// SimpleAmbientGroundingGate is the default ambient ledger correlator.
type SimpleAmbientGroundingGate struct {
	cfg GroundingConfig
}

func NewSimpleAmbientGroundingGate(cfg GroundingConfig) *SimpleAmbientGroundingGate {
	if cfg.PostTurn.Mode == "" {
		cfg = DefaultGroundingConfig()
	}
	return &SimpleAmbientGroundingGate{cfg: cfg}
}

// CheckTurn evaluates ambient implement sessions for grounding gaps. With workers it
// correlates queue state and summary tags (worker finalize parity); with no workers it
// reads the coordinator's own citation audit. It never blocks — the verdict drives an
// advisory counter that flags after a threshold (see AfterPrompt).
func (g *SimpleAmbientGroundingGate) CheckTurn(_ context.Context, in AmbientGroundingInput) GroundingVerdict {
	mode := g.cfg.AmbientPostTurnMode()
	if mode == "off" {
		return GroundingVerdict{OK: true}
	}
	if len(in.Jobs) == 0 {
		if in.LastAuditUngrounded {
			code := strings.TrimSpace(in.LastAuditCode)
			if code == "" {
				code = ambientUngroundedCompletionCode
			}
			return GroundingVerdict{
				OK:     false,
				Code:   code,
				Reason: "coordinator citations did not trace to tool evidence",
			}
		}
		return GroundingVerdict{OK: true}
	}
	if containsTool(in.LastTurnTools, "task") ||
		containsTool(in.LastTurnTools, "wait") ||
		containsTool(in.LastTurnTools, "delegate_dispatch") {
		return GroundingVerdict{OK: true}
	}
	if ambientLedgerSatisfied(in.Jobs, in.SummaryTags) {
		return GroundingVerdict{OK: true}
	}
	return GroundingVerdict{
		OK:     false,
		Code:   ambientUngroundedCompletionCode,
		Reason: "worker job ledger not satisfied on parent session",
	}
}

func ambientLedgerSatisfied(jobs []api.WorkerTask, tags []WorkerSummaryTag) bool {
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

func hasSummaryForJob(tags []WorkerSummaryTag, jobID string) bool {
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

// AmbientGroundingCoordinator wires ambient grounding into session prompts.
type AmbientGroundingCoordinator struct {
	Store    Store
	Queue    ambientWorkerQueue
	Gate     AmbientGroundingGate
	Config   GroundingConfig
	State    *grounding.StateStore
	Sessions *session.Host
	Events   *events.Publisher
	// Pipeline is required for ambient post-turn Decisions (coordinator.closeout_check).
	Pipeline *oar.GuardPipeline
}

func NewAmbientGroundingCoordinator(store Store, queue ambientWorkerQueue, gate AmbientGroundingGate, cfg GroundingConfig, state *grounding.StateStore, sessions *session.Host) *AmbientGroundingCoordinator {
	if gate == nil {
		gate = NewSimpleAmbientGroundingGate(cfg)
	}
	if state == nil {
		state = grounding.NewStateStore()
	}
	return &AmbientGroundingCoordinator{
		Store:    store,
		Queue:    queue,
		Gate:     gate,
		Config:   cfg,
		State:    state,
		Sessions: sessions,
	}
}

// IsEscalated reports circuit breaker block for an ambient session.
func (g *AmbientGroundingCoordinator) IsEscalated(sessionID string) bool {
	if g == nil || g.State == nil {
		return false
	}
	return g.State.IsEscalated(ambientStateKey(sessionID))
}

// Reset clears ambient grounding counters after successful delegation.
func (g *AmbientGroundingCoordinator) Reset(sessionID string) {
	if g != nil && g.State != nil {
		g.State.Reset(ambientStateKey(sessionID))
	}
}

// AfterPrompt runs post-turn ambient grounding when no delegation is active for the session.
func (g *AmbientGroundingCoordinator) AfterPrompt(ctx context.Context, sessionID string, lastTools []string) error {
	if g == nil || g.Gate == nil {
		return nil
	}
	if g.Store != nil {
		if _, ok := g.Store.DelegationBySessionID(sessionID); ok {
			return nil
		}
	}
	in, err := g.buildInput(ctx, sessionID, lastTools)
	if err != nil {
		return err
	}
	verdict := g.Gate.CheckTurn(ctx, in)
	key := ambientStateKey(sessionID)
	if g.Pipeline == nil || !g.Pipeline.AnchorEnforced(oar.AnchorCoordinatorPostTurn) {
		return nil
	}
	return g.applyOARAmbientGrounding(ctx, sessionID, key, in, verdict)
}

func (g *AmbientGroundingCoordinator) applyOARAmbientGrounding(ctx context.Context, sessionID, key string, in AmbientGroundingInput, verdict GroundingVerdict) error {
	if verdict.OK {
		if containsTool(in.LastTurnTools, "task") {
			g.Reset(sessionID)
			if store := g.Pipeline.Counters(); store != nil {
				store.Reset(sessionID, ambientUngroundedCompletionCode, oar.CounterBreaker)
				store.Reset(sessionID, ambientGroundingEscalatedCode, oar.CounterBreaker)
			}
		}
		return nil
	}
	gc := oar.NewGuardContext()
	gc.SessionID = sessionID
	gc.Profile = "coordinator"
	ObserveDelegationGroundingVerdict(gc, verdict)
	gc.GroundingEscalated = groundingFlagged(g.State.Get(key), g.Config)
	missing := []string{}
	for _, job := range in.Jobs {
		if job.Status == api.WorkerStatusComplete && !hasSummaryForJob(in.SummaryTags, job.ID) {
			missing = append(missing, job.ID)
		}
	}
	gc.PutRejectData(ambientUngroundedCompletionCode, map[string]any{"missing_summary_job_ids": missing})
	_, err := g.Pipeline.EvaluateBlock(ctx, oar.AnchorCoordinatorPostTurn, gc)
	if err != nil {
		return err
	}
	state := g.State.Get(key)
	if store := g.Pipeline.Counters(); store != nil {
		n := store.Get(sessionID, ambientUngroundedCompletionCode, oar.CounterBreaker)
		state.ConsecutiveWarnings = int(n)
		state.TotalWarnings = int(n)
		g.State.Set(key, state)
	}
	flagged := groundingFlagged(state, g.Config)
	if g.Events != nil {
		code := verdict.Code
		if flagged {
			code = ambientGroundingEscalatedCode
		}
		g.Events.PublishGrounding(ctx, in.ProjectID, sessionID, code, "", "", flagged)
	}

	return nil
}

// groundingFlagged reports whether accumulated advisory warnings have crossed the
// circuit-breaker threshold. It mirrors the breaker thresholds but never blocks.
func groundingFlagged(state grounding.UngroundedCounter, cfg GroundingConfig) bool {
	cb := cfg.CircuitBreaker
	if cb.MaxConsecutiveUngrounded > 0 && state.ConsecutiveWarnings >= cb.MaxConsecutiveUngrounded {
		return true
	}
	return cb.MaxUngroundedWarnings > 0 && state.TotalWarnings >= cb.MaxUngroundedWarnings
}

// lastAssistantAudit returns the citation-grounding audit on the most recent assistant
// message, or nil when the last prose turn had nothing to ground.
func lastAssistantAudit(msgs []api.Message) *api.CitationGrounding {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == api.MessageRoleAssistant {
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
	msgs, _ := g.Sessions.Transcript.GetMessages(ctx, sessionID)
	in.SummaryTags = summaryTagsFromMessages(msgs)
	if audit := lastAssistantAudit(msgs); audit != nil && !audit.Traced {
		in.LastAuditUngrounded = true
		in.LastAuditCode = strings.TrimSpace(audit.HintCode)
	}
	in.UngroundedState = g.State.Get(ambientStateKey(sessionID))
	return in, nil
}

func summaryTagsFromMessages(msgs []api.Message) []WorkerSummaryTag {
	var out []WorkerSummaryTag
	for _, msg := range msgs {
		if msg.WorkerSummary == nil {
			continue
		}
		out = append(out, WorkerSummaryTag{
			DelegationID: msg.WorkerSummary.DelegationID,
			LegID:        msg.WorkerSummary.LegID,
			JobID:        msg.WorkerSummary.WorkerID,
			MessageID:    msg.ID,
			TS:           msg.CreatedAt,
		})
	}
	return out
}
