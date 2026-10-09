package runtime

import (
	"context"
	"encoding/json"
	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
	workflowblueprintfiles "github.com/lycaon/lycaon/internal/workflow/blueprintfiles"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	workflowgates "github.com/lycaon/lycaon/internal/workflow/gates"
	workflowpresentation "github.com/lycaon/lycaon/internal/workflow/presentation"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/pkg/api"
	"strings"
)

// Project projects state from one loaded workflow revision.
func (m *Snapshots) Project(
	ctx context.Context,
	active *api.WorkflowRun,
	manifest workflowdef.Manifest,
	vars map[string]any,
) inject.WorkflowRuntimeSnapshot {
	snap := inject.WorkflowRuntimeSnapshot{
		Topology: strings.TrimSpace(manifest.Topology),
	}
	if manifest.Blueprint != nil || runstate.RunHasBlueprint(active) {
		if sess, err := m.Sessions.Get(ctx, active.SessionID); err == nil && sess != nil {
			blueprintID := ""
			var frontmatter []string
			if manifest.Blueprint != nil {
				blueprintID = manifest.Blueprint.ID
				frontmatter = manifest.Blueprint.Frontmatter
			}
			fields := map[string]string{}
			rel := strings.TrimSpace(active.BlueprintPath)
			if rel != "" && sess.WorkspacePath != "" {
				if content, err := workflowblueprintfiles.ReadBlueprintFile(sess.WorkspacePath, rel); err == nil {
					fields = workflowblueprintfiles.ParsePlanBlueprintFrontmatter(content, frontmatter)
					snap.BlueprintBody = content
				}
			} else if rel != "" {
				if loaded, err := workflowblueprintfiles.LoadBlueprintView(sess.WorkspacePath, &workflowdef.BlueprintDef{
					ID:          blueprintID,
					Path:        rel,
					Frontmatter: frontmatter,
				}); err == nil {
					fields = loaded
				}
			}
			view := &inject.BlueprintView{
				Path: rel,
			}
			for _, key := range frontmatter {
				val := fields[key]
				if val == "" {
					if v, ok := runstate.DotPathString(vars, "artifact."+blueprintID+"."+key); ok {
						val = v
					}
				}
				if val != "" {
					view.Frontmatter = append(view.Frontmatter, inject.BlueprintField{Key: key, Value: val})
				}
			}
			snap.Blueprint = view
		}
	}
	if workflowdef.RunHasParent(active) && runstate.RunHasBlueprint(active) && manifest.Blueprint == nil {
		snap.BlueprintApproval = m.Approvals.InheritedApprovalSnapshot(ctx, active)
	}
	for _, phase := range manifest.PhaseDefs {
		if phase.ID != active.CurrentPhase || phase.ReviewLoop == nil || !phase.ReviewLoop.CarriesCoverage() {
			continue
		}
		facts, err := m.Coverage.CoverageFacts(ctx, active, manifest)
		if err != nil {
			snap.CoverageReview = "Coverage facts unavailable: " + err.Error()
		} else {
			prior := workflowpresentation.RunCoverageReview(workflowpresentation.ReviewVerdicts(ctx, m.Verdicts, active, manifest))
			raw, marshalErr := json.Marshal(struct {
				Facts any `json:"facts"`
				Prior any `json:"prior_review,omitempty"`
			}{facts, prior})
			if marshalErr == nil {
				snap.CoverageReview = string(raw)
			}
		}
		break
	}
	var currentGates []workflowpresentation.PhaseGateSnapshot
	for _, id := range manifest.Phases {
		def, ok := manifest.PhaseForRun(active, id)
		if !ok {
			continue
		}
		row := inject.WorkflowPhaseRow{
			ID:                def.ID,
			CompleteWhen:      def.CompleteWhen,
			BindTopologyStage: def.BindTopologyStage,
			Terminal:          def.Terminal,
		}
		if next, ok := manifest.ResolveAdvanceTargetForRun(active, def.ID); ok {
			row.Next = next
		}
		gateSnaps := make([]workflowpresentation.PhaseGateSnapshot, 0, len(def.Gates))
		for _, gate := range def.Gates {
			gateSnaps = append(gateSnaps, workflowpresentation.PhaseGateSnapshot{
				ID:        gate,
				Satisfied: m.gateLeafSatisfied(ctx, active, def, vars, gate),
				Dormant:   !m.gateLeafApplicable(ctx, active, def, vars, gate),
			})
		}
		for _, gs := range gateSnaps {
			row.Gates = append(row.Gates, inject.WorkflowGateState{
				ID:        gs.ID,
				Satisfied: gs.Satisfied,
				Dormant:   gs.Dormant,
			})
		}
		if def.ID == active.CurrentPhase {
			currentGates = gateSnaps
		}
		snap.Phases = append(snap.Phases, row)
	}
	if def, ok := manifest.PhaseForRun(active, active.CurrentPhase); ok {
		snap.ReportDocumentEnabled = manifest.ReportEnabled() && workflowdef.PhaseHasGate(def, "topology_report_delivered")
		snap.CloseoutRetries = def.CloseoutRetries
		if brief := manifest.ReportBrief(); snap.ReportDocumentEnabled && brief != nil {
			snap.ReportRating = &inject.ReportRatingView{Dimensions: brief.DimensionIDs(), Questions: brief.PromptText()}
		}
		if plan, found := runstate.FanoutPlanForPhase(vars, def); found {
			snap.FanoutPlan = runstate.FormatFanoutPlan(plan)
			if coverage, ok := vars["fanout_coverage"]; ok {
				if data, err := surveyjson.Marshal(coverage); err == nil {
					snap.FanoutPlan += "\n" + string(data)
				}
			}
		}
		var reviewAgents []string
		if def.ReviewLoop != nil {
			var captured bool
			reviewAgents, captured = runstate.EffectiveReviewAgents(active.CurrentPhase, *def.ReviewLoop, vars)
			if !captured {
				reviewAgents = runstate.ReviewLoopDeclaredAgents(*def.ReviewLoop)
			}
		}
		exit := workflowpresentation.ProjectPhaseExit(manifest, def, currentGates, reviewAgents)
		snap.PhaseExit = exit.InjectView()
	}
	return snap
}

func (m *Snapshots) gateLeafApplicable(ctx context.Context, run *api.WorkflowRun, def workflowdef.PhaseDef, vars map[string]any, gate string) bool {
	gate = strings.TrimSpace(gate)
	if gate == "" || m == nil || m.Registry == nil || run == nil {
		return true
	}
	var sess *api.Session
	if m.Sessions != nil && run.SessionID != "" {
		sess, _ = m.Sessions.Get(ctx, run.SessionID)
	}
	ec := conditions.EvalContextFromRun(ctx, sess, run, vars)
	ec.Phase = def.ID
	ec.BindTopologyStage = def.BindTopologyStage
	ec.BindParallelGroup = append([]string(nil), def.BindParallelGroup...)
	applicable, err := m.Registry.Applicable(gate, ec)
	if err != nil {
		return true
	}
	return applicable
}

// gateLeafSatisfied uses live evaluation for advance parity.
func (m *Snapshots) gateLeafSatisfied(ctx context.Context, run *api.WorkflowRun, def workflowdef.PhaseDef, vars map[string]any, gate string) bool {
	gate = strings.TrimSpace(gate)
	if gate == "" {
		return true
	}
	if m != nil && m.Registry != nil && run != nil {
		var sess *api.Session
		if m.Sessions != nil && run.SessionID != "" {
			sess, _ = m.Sessions.Get(ctx, run.SessionID)
		}
		ec := conditions.EvalContextFromRun(ctx, sess, run, vars)
		ec.Phase = def.ID
		ec.BindTopologyStage = def.BindTopologyStage
		ec.BindParallelGroup = append([]string(nil), def.BindParallelGroup...)
		ok, err := m.Registry.Evaluate(gate, ec)
		if err == nil {
			return ok
		}
		if !conditions.IsUnknownCondition(err) {
			return false
		}
	}
	return workflowgates.SatisfiedInVars(vars, gate)
}

type Snapshots struct {
	Sessions  Sessions
	Registry  *conditions.ConditionRegistry
	Approvals ApprovalState
	Coverage  CoverageFacts
	Verdicts  workflowpresentation.ReviewEvidenceLister
}
