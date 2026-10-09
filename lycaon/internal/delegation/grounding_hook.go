package delegation

import (
	"context"

	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/grounding"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/scan"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/pkg/api"
)

// GroundingCoordinator wires delegation grounding into session prompts.
type GroundingCoordinator struct {
	Store             Store
	Queue             workerQueueSnapshot
	Gate              DelegationGroundingGate
	Config            GroundingConfig
	State             *grounding.StateStore
	Sessions          *session.Host
	InspectorCloseout *InspectorCloseoutGate
	Events            *events.Publisher
	// Pipeline evaluates closeout refusals and post-turn advisories.
	Pipeline *oar.GuardPipeline
}

type workerQueueSnapshot interface {
	List(ctx context.Context, projectDir string, statuses ...api.WorkerStatus) ([]api.WorkerTask, error)
}

func NewGroundingCoordinator(store Store, queue workerQueueSnapshot, gate DelegationGroundingGate, cfg GroundingConfig, state *grounding.StateStore, sessions *session.Host) *GroundingCoordinator {
	if gate == nil {
		gate = NewSimpleDelegationGroundingGate(cfg)
	}
	if state == nil {
		state = grounding.NewStateStore()
	}
	return &GroundingCoordinator{
		Store:    store,
		Queue:    queue,
		Gate:     gate,
		Config:   cfg,
		State:    state,
		Sessions: sessions,
	}
}

func (g *GroundingCoordinator) IsEscalated(sessionID string) bool {
	if g == nil || g.State == nil {
		return false
	}
	return g.State.IsEscalated(sessionID)
}

// Reset clears grounding counters after successful delegation.
func (g *GroundingCoordinator) Reset(sessionID string) {
	if g != nil && g.State != nil {
		g.State.Reset(sessionID)
		if g.Pipeline != nil {
			g.Pipeline.Counters().Reset(sessionID, "COORDINATOR_UNGROUNDED_CLAIM", oar.CounterBreaker)
		}
	}
}

// AfterPrompt checks grounding during delegation setup and worker phases.
func (g *GroundingCoordinator) AfterPrompt(ctx context.Context, sessionID string, lastTools []string) error {
	if g == nil || g.Gate == nil || g.Store == nil {
		return nil
	}
	delegationID, ok := g.Store.DelegationBySessionID(sessionID)
	if !ok {
		return nil
	}
	delegation, err := g.Store.Get(ctx, delegationID)
	if err != nil {
		return err
	}
	if delegation.Phase != api.DelegationPhaseSetup && delegation.Phase != api.DelegationPhaseWorker {
		return nil
	}
	in, err := g.buildInput(ctx, sessionID, delegationID, lastTools)
	if err != nil {
		return err
	}
	if shouldResetDelegationGrounding(lastTools) {
		g.Reset(sessionID)
		in.UngroundedState = g.State.Get(sessionID)
	}
	verdict := g.Gate.CheckTurn(ctx, in)
	if verdict.OK {
		return nil
	}
	// Aggregate friction includes rejects from different grounding rules.
	if g.Sessions != nil {
		g.Sessions.Runner.Closeouts.RecordGroundingFriction(ctx, sessionID)
	}
	if g.Pipeline == nil || !g.Pipeline.AnchorEnforced(oar.AnchorCoordinatorPostTurn) {
		return nil
	}
	gc := oar.NewGuardContext()
	gc.SessionID = sessionID
	gc.Profile = "coordinator"
	ObserveDelegationGroundingVerdict(gc, verdict)
	if _, err := g.Pipeline.EvaluateBlock(ctx, oar.AnchorCoordinatorPostTurn, gc); err != nil {
		return err
	}
	g.publishGroundingVerdict(ctx, sessionID, delegationID, verdict)
	if g.IsEscalated(sessionID) {
		return guidance.ErrGroundingEscalated
	}
	return nil
}

func (g *GroundingCoordinator) rejectCloseoutGrounding(ctx context.Context, sessionID, delegationID string, verdict GroundingVerdict) error {
	if g.Pipeline == nil || !g.Pipeline.AnchorEnforced(oar.AnchorCoordinatorCloseoutCheck) {
		return ErrGroundingPending
	}
	gc := oar.NewGuardContext()
	gc.SessionID = sessionID
	gc.Profile = "coordinator"
	ObserveDelegationGroundingVerdict(gc, verdict)
	res, err := g.Pipeline.EvaluateBlock(ctx, oar.AnchorCoordinatorCloseoutCheck, gc)
	if err != nil {
		return err
	}
	state := g.State.Get(sessionID)
	n := g.Pipeline.Counters().Get(sessionID, "COORDINATOR_UNGROUNDED_CLAIM", oar.CounterBreaker)
	state.ConsecutiveWarnings = int(n)
	state.TotalWarnings = int(n)
	if g.Config.CircuitBreaker.EscalateMode == "block" && groundingFlagged(state, g.Config) {
		state.Escalated = true
	}
	g.State.Set(sessionID, state)
	g.publishGroundingVerdict(ctx, sessionID, delegationID, verdict)
	if res == nil || res.Decision == nil || res.Decision.Effect != oar.EffectBlock {
		return ErrGroundingPending
	}
	d := res.Decision
	body, err := guidance.RenderPolicyCopy(ctx, d.Code, string(d.Effect), d.Copy)
	if err != nil {
		return ErrGroundingPending
	}
	return guidance.NewRefusal(d.Code, body).WithPolicyCopy(d.Copy).WithDetails(d.Data, nil).WithCause(ErrGroundingPending)
}

func (g *GroundingCoordinator) publishGroundingVerdict(ctx context.Context, sessionID, delegationID string, verdict GroundingVerdict) {
	if g.Events == nil {
		return
	}
	delegation, err := g.Store.Get(ctx, delegationID)
	if err != nil || delegation == nil {
		return
	}
	g.Events.PublishGrounding(ctx, delegation.ProjectID, sessionID, verdict.Code, verdict.LegID, delegationID, g.IsEscalated(sessionID))
}

func (g *GroundingCoordinator) CheckDelegationCloseout(ctx context.Context, delegationID string) error {
	if g == nil || g.Gate == nil {
		return nil
	}
	if g.Config.Closeout.Mode == "off" {
		return nil
	}
	if g.InspectorCloseout != nil {
		if err := g.InspectorCloseout.Check(ctx, delegationID); err != nil {
			if pending, ok := scan.AsGatePending(err); ok {
				g.publishGatePending(ctx, delegationID, pending)
				return scan.ErrGatePending
			}
			return ErrGroundingPending
		}
	}
	sessionID, ok := g.Store.SessionID(delegationID)
	if !ok {
		return ErrGroundingPending
	}
	in, err := g.buildInput(ctx, sessionID, delegationID, nil)
	if err != nil {
		return err
	}
	verdict := g.Gate.CheckCloseout(ctx, in)
	if verdict.OK {
		return nil
	}
	return g.rejectCloseoutGrounding(ctx, sessionID, delegationID, verdict)
}

func (g *GroundingCoordinator) buildInput(ctx context.Context, sessionID, delegationID string, lastTools []string) (GroundingInput, error) {
	delegation, err := g.Store.Get(ctx, delegationID)
	if err != nil {
		return GroundingInput{}, err
	}
	legs, err := g.Store.ListLegs(ctx, delegationID)
	if err != nil {
		return GroundingInput{}, err
	}
	var jobs []api.WorkerTask
	if g.Queue != nil {
		jobs, _ = g.Queue.List(ctx, delegation.ProjectID)
	}
	filtered := make([]api.WorkerTask, 0)
	for _, j := range jobs {
		if j.DelegationID == delegationID {
			filtered = append(filtered, j)
		}
	}
	tags := g.summaryTags(ctx, sessionID)
	state := g.State.Get(sessionID)
	return GroundingInput{
		SessionID:       sessionID,
		DelegationID:    delegationID,
		Delegation:      *delegation,
		Legs:            legs,
		Jobs:            filtered,
		SummaryTags:     tags,
		LastTurnTools:   append([]string(nil), lastTools...),
		UngroundedState: state,
	}, nil
}

func (g *GroundingCoordinator) summaryTags(ctx context.Context, sessionID string) []WorkerSummaryTag {
	if g.Sessions == nil {
		return nil
	}
	msgs, err := g.Sessions.Transcript.GetMessages(ctx, sessionID)
	if err != nil {
		return nil
	}
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

func (g *GroundingCoordinator) publishGatePending(ctx context.Context, delegationID string, pending *scan.GatePendingError) {
	if g == nil || g.Events == nil || pending == nil {
		return
	}
	delegation, err := g.Store.Get(ctx, delegationID)
	if err != nil || delegation == nil {
		return
	}
	sessionID, _ := g.Store.SessionID(delegationID)
	branch := branchInstructionFromGuidance(pending.Guidance)
	g.Events.PublishGatePending(ctx, delegation.ProjectID, sessionID, api.WorkflowEvent{
		Event:             api.WorkflowEventKindGatePending,
		BranchInstruction: branch,
		Guidance:          pending.Guidance,
	})
}

func branchInstructionFromGuidance(in []api.ScanGuidanceSummary) string {
	if len(in) == 0 {
		return "Resolve security scan findings before closeout."
	}
	g := in[0]
	if g.Fix != "" {
		return g.Fix
	}
	return g.Message
}

func shouldResetDelegationGrounding(lastTools []string) bool {
	for _, t := range lastTools {
		if t == "delegate_dispatch" || t == "task" {
			return true
		}
	}
	return false
}
