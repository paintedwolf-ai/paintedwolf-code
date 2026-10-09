package runtime

import (
	"context"
	"log/slog"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/spawn"
	workflowcatalog "github.com/lycaon/lycaon/internal/workflow/catalog"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	workflowdrafts "github.com/lycaon/lycaon/internal/workflow/drafts"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/internal/workflow/verdictcall"
	"github.com/lycaon/lycaon/pkg/api"
)

// CoordinatorFrames loads workflow state for coordinator turns.
type CoordinatorFrames struct {
	Runs         runstate.RunsRepository
	Resolver     *workflowcatalog.Resolver
	Snapshots    *Snapshots
	Policy       *SessionPolicy
	Obligations  ObligationHolds
	SessionStore workflowdrafts.Store
	ConfigRoot   string
	// VerdictCatalog is a session's effective submit_verdict schema, which a
	// project pack may customize; a review phase composes its call from it.
	VerdictCatalog func(ctx context.Context, sessionID string) map[string]any
}

// BuildCoordinatorTurnFrame loads one workflow revision.
func (l *CoordinatorFrames) BuildCoordinatorTurnFrame(
	ctx context.Context,
	sessionID string,
	sess *api.Session,
) (inject.CoordinatorTurnFrame, error) {
	if l == nil || l.Runs == nil {
		return inject.CoordinatorTurnFrame{}, nil
	}
	out := api.CoordinatorRunContext{}
	var records []workflowdrafts.Record

	if l.SessionStore != nil {
		loaded, err := l.SessionStore.ListBySession(ctx, sessionID)
		if err != nil {
			return inject.CoordinatorTurnFrame{RunContext: out}, err
		}
		records = loaded
		out.HasComposeDraft = len(records) > 0
		summary, ok := workflowdrafts.LatestEffectiveSummary(records)
		if ok {
			out.CoordinatorBrief = summary.CoordinatorBrief
			out.RequiresIsolation = summary.RequiresIsolation
			out.FeedbackPhases = append([]api.ComposeFeedbackPhase(nil), summary.Feedback...)
			out.DecisionPhases = append([]api.ComposeDecisionPhase(nil), summary.Decisions...)
		}
	}

	active, vars, err := l.Runs.ActiveStateBySession(ctx, sessionID)
	if err != nil {
		return inject.CoordinatorTurnFrame{RunContext: out}, err
	}
	if active == nil {
		out.AllowedAgents = spawn.AmbientAllowedAgents()
		return inject.CoordinatorTurnFrame{RunContext: out}, nil
	}
	manifest, err := l.Resolver.ForRun(ctx, active)
	if err != nil {
		return inject.CoordinatorTurnFrame{WorkflowRevision: active.Revision, RunContext: out}, err
	}
	runtime := l.Snapshots.Project(ctx, active, manifest, vars)
	l.attachPhaseVerdictCall(ctx, sessionID, active, manifest, runtime.PhaseExit)
	out.WorkflowID = active.WorkflowID
	out.WorkflowVersion = active.WorkflowVersion
	out.CurrentPhase = active.CurrentPhase
	out.RunID = active.ID
	out.RunStatus = string(active.Status)
	out.AllowedAgents = l.Policy.RosterFor(active, manifest)
	if request, ok := runstate.RequestStateFromVars(vars); ok {
		out.Request = request
	}
	if key, _ := hostStringVar(vars, "workflow_compose_summary_id"); key != "" {
		if summary, ok := workflowdrafts.EffectiveSummaryByKey(records, key); ok {
			out.CoordinatorBrief = summary.CoordinatorBrief
			out.RequiresIsolation = summary.RequiresIsolation
			out.FeedbackPhases = append([]api.ComposeFeedbackPhase(nil), summary.Feedback...)
			out.DecisionPhases = append([]api.ComposeDecisionPhase(nil), summary.Decisions...)
		}
	}
	out.FailedLeaves = hostStringSliceVar(vars, "last_failed_leaves")
	if len(out.FailedLeaves) > 0 {
		out.FailedLeaves = filterPersistedFailedLeaves(out.FailedLeaves, active.CurrentPhase, runtime)
	}
	if mode, ok := workflowdef.ScaffoldExecutionModeStamp(vars); ok {
		out.PhaseExecutionMode = mode
	} else {
		if mode, ok := workflowdef.WorkflowDefaultForceMode(manifest, vars); ok {
			out.WorkflowDefaultExecutionMode = mode
		}
	}
	out.SurfaceProfile = strings.TrimSpace(manifest.SurfaceProfile)
	if binding, err := workflowdef.ResolveSurfaceBinding(manifest, active.CurrentPhase, active.WorkflowID, l.ConfigRoot); err == nil {
		out.PhaseCoordinatorSurface = binding.CoordinatorSurface
		out.PhaseSurfaceTemplate = binding.SurfaceTemplate
		out.PhaseModeRefs = append([]string(nil), binding.ModeRefs...)
		if binding.ProfileResolved {
			eligible := binding.WorkflowInvestigateEligible
			out.WorkflowInvestigateEligible = &eligible
		}
	}
	if def, ok := manifest.PhaseForRun(active, active.CurrentPhase); ok {
		out.AdvanceWhenGateMet = string(workflowdef.EffectiveAdvancePolicy(manifest, def))
	}
	// A read error leaves the phase unheld, so the turn can report the failure.
	if held, heldErr := l.Obligations.HostObligationHeld(ctx, sessionID); heldErr == nil {
		out.PhaseHostHeld = held
	}
	if pending, ok := runstate.PendingFeedbackFromVars(vars); ok {
		p := pending
		out.PendingFeedback = &p
	}
	return inject.CoordinatorTurnFrame{
		WorkflowRevision:     active.Revision,
		RunContext:           out,
		Runtime:              runtime,
		ManifestRules:        append([]string(nil), manifest.Rules...),
		ScaffoldVars:         runstate.CloneVars(vars),
		EvidenceRequirements: workflowdef.PhaseEvidenceRequirements(manifest, active.CurrentPhase),
	}, nil
}

func filterPersistedFailedLeaves(
	failed []string,
	currentPhase string,
	snap inject.WorkflowRuntimeSnapshot,
) []string {
	if len(failed) == 0 {
		return nil
	}
	for _, phase := range snap.Phases {
		if phase.ID != currentPhase {
			continue
		}
		// A complete_when phase keeps its advance denial leaves.
		if len(phase.Gates) == 0 {
			return append([]string(nil), failed...)
		}
		live := inject.UnsatisfiedGateIDs(phase.Gates)
		if len(live) == 0 {
			return nil
		}
		liveSet := make(map[string]struct{}, len(live))
		for _, leaf := range live {
			liveSet[leaf] = struct{}{}
		}
		out := make([]string, 0, len(failed))
		for _, leaf := range failed {
			leaf = strings.TrimSpace(leaf)
			if _, ok := liveSet[leaf]; ok {
				out = append(out, leaf)
			}
		}
		return out
	}
	return append([]string(nil), failed...)
}

func hostStringVar(vars map[string]any, key string) (string, bool) {
	if vars == nil {
		return "", false
	}
	if v, ok := vars[key]; ok {
		if s, ok := v.(string); ok {
			return strings.TrimSpace(s), s != ""
		}
	}

	return "", false
}

func hostStringSliceVar(vars map[string]any, key string) []string {
	raw, ok := vars[key]
	if !ok {
		return nil
	}
	switch v := raw.(type) {
	case []string:
		return append([]string(nil), v...)
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

// recordGateFailure stores failed leaves for prompt projection.

var _ inject.CoordinatorTurnFrameSource = (*CoordinatorFrames)(nil)

// attachPhaseVerdictCall gives a review phase's exit the submit_verdict call it
// accepts, composed from the session's effective catalog.
func (l *CoordinatorFrames) attachPhaseVerdictCall(ctx context.Context, sessionID string, active *api.WorkflowRun, manifest workflowdef.Manifest, exit *inject.PhaseExitView) {
	if exit == nil || l.VerdictCatalog == nil {
		return
	}
	def, ok := manifest.PhaseForRun(active, active.CurrentPhase)
	if !ok || def.ReviewLoop == nil {
		return
	}
	catalog := l.VerdictCatalog(ctx, sessionID)
	if catalog == nil {
		return
	}
	if err := verdictcall.Attach(exit, catalog, *def.ReviewLoop, manifest.ReportBrief()); err != nil {
		slog.ErrorContext(ctx, "compose phase verdict call", "run_id", active.ID, "phase", def.ID, "error", err)
	}
}
