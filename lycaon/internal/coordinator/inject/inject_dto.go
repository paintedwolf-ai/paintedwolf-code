package inject

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"strings"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/guidance/feedback"
	"github.com/lycaon/lycaon/pkg/api"
)

// ActiveWorkflowInjectSentinel marks active workflow renders.
const ActiveWorkflowInjectSentinel = "<!-- lycaon-workflow-runtime:v1 -->"

const maxPhaseIDPreview = 4

// PhaseExitView is the current phase's leave recipe.
type PhaseExitView struct {
	Kind                string
	DepthParam          string
	CoordinatorAdvances bool
	OpenGates           []string
	DormantGates        []string
	CompleteWhen        string
	VerdictSchema       map[string]string
	ClaimStatuses       []string
	ReviewLoopKey       string
	ReviewLoopCap       int
	// ReviewAgents is the verdict-owed reviewer roster for a review_loop phase.
	ReviewAgents      []string
	HumanApproval     bool
	InvokeWorkflowID  string
	ChoiceTransitions []PhaseExitChoiceArm
}

// PhaseExitChoiceArm is one declared choice transition for inject teaching.
type PhaseExitChoiceArm struct {
	ID     string
	Label  string
	Actors []string
	// CoordinatorMayFire reports whether the coordinator is a declared actor,
	// so the template can tell it to call workflow_transition or to stand down.
	CoordinatorMayFire bool
}

// WorkflowRuntimeSnapshot is workflow-generic runtime metadata for inject.
type WorkflowRuntimeSnapshot struct {
	ReportDocumentEnabled bool
	// ReportRating is the rating a report document answers, when the workflow
	// declares one.
	ReportRating      *ReportRatingView
	Topology          string
	Phases            []WorkflowPhaseRow
	Blueprint         *BlueprintView
	BlueprintApproval *BlueprintApprovalView
	// FanoutPlan is rendered only while the current phase permits worker execution.
	FanoutPlan string
	// BlueprintBody supplies gate diagnostics.
	BlueprintBody string
	PhaseExit     *PhaseExitView
}

// BlueprintApprovalView is the authoritative approval state projected into a child run.
type BlueprintApprovalView struct {
	Status      string
	Origin      string
	ParentRunID string
}

// WorkflowGateState is one gate's live state.
// Dormant gates block advancement without rendering an obligation.
type WorkflowGateState struct {
	ID        string
	Satisfied bool
	Dormant   bool
}

// WorkflowPhaseRow carries live phase state for the template.
type WorkflowPhaseRow struct {
	ID                string
	CompleteWhen      string
	BindTopologyStage string
	Gates             []WorkflowGateState
	Next              string
	Terminal          bool
}

// ActiveWorkflowPhaseView is one phase entry in the inject DTO.
type ActiveWorkflowPhaseView struct {
	ID        string
	IsCurrent bool
	Terminal  bool
}

// PendingFeedbackView is open-ended user feedback awaiting response.
type PendingFeedbackView struct {
	PhaseID string
	Prompt  string
}

type WorkflowRequestView struct {
	Status   string
	Text     string
	Source   string
	Sequence int
}

// ReportRatingView is a workflow's declared rating as a report fence answers it.
type ReportRatingView struct {
	// Dimensions are the answer keys, in declared order.
	Dimensions []string
	// Questions list each dimension with its question and allowed answers.
	Questions string
}

// ActiveWorkflowInjectData is the pongo data model for inject/active-workflow.md.
type ActiveWorkflowInjectData struct {
	ReportDocumentEnabled bool
	ReportRating          *ReportRatingView
	WorkflowID            string
	WorkflowVersion       string
	RunID                 string
	RunStatus             string
	CurrentPhase          string
	CompleteWhen          string
	Topology              string
	TopologyPhaseID       string
	RequiresIsolation     bool
	CoordinatorBrief      string
	FanoutPlan            string
	FailedLeaves          []string
	UnsatisfiedGateLeaves []string
	GateObligations       []GateObligationView
	// AllowedAgents is the effective dispatchable roster.
	// ExcludedAgents includes each dispatch rejection code.
	AllowedAgents     []string
	ExcludedAgents    []ExcludedAgent
	PendingFeedback   *PendingFeedbackView
	Request           *WorkflowRequestView
	FeedbackPhases    []string
	DecisionPhases    []string
	Phases            []ActiveWorkflowPhaseView
	PhaseIndex        int // 1-based position of the current phase
	PhaseTotal        int
	NextPhase         string
	CurrentGates      []WorkflowGateState
	PhaseExit         *PhaseExitView
	BlueprintApproval *BlueprintApprovalView
}

// GateObligationView is one unsatisfied leaf's compact gate-feedback projection.
type GateObligationView struct {
	ID       string
	Purpose  string
	Satisfy  []string
	Missing  []string
	Required []string
}

// UnsatisfiedGateIDs returns current-phase gate ids that are not yet satisfied.
func UnsatisfiedGateIDs(gates []WorkflowGateState) []string {
	if len(gates) == 0 {
		return nil
	}
	out := make([]string, 0, len(gates))
	for _, g := range gates {
		id := strings.TrimSpace(g.ID)
		if id == "" || g.Satisfied || g.Dormant {
			continue
		}
		out = append(out, id)
	}
	return out
}

// BuildActiveWorkflowInjectData maps a turn frame into the workflow inject.
func BuildActiveWorkflowInjectData(frame CoordinatorTurnFrame) ActiveWorkflowInjectData {
	runCtx := frame.RunContext
	snap := frame.Runtime
	failed := append([]string(nil), runCtx.FailedLeaves...)
	allowed := append([]string(nil), runCtx.AllowedAgents...)
	var excluded []ExcludedAgent
	if frame.Roster != nil {
		allowed = append([]string(nil), frame.Roster.Effective...)
		excluded = append([]ExcludedAgent(nil), frame.Roster.Excluded...)
	}
	out := ActiveWorkflowInjectData{
		WorkflowID:            strings.TrimSpace(runCtx.WorkflowID),
		WorkflowVersion:       strings.TrimSpace(runCtx.WorkflowVersion),
		RunID:                 strings.TrimSpace(runCtx.RunID),
		ReportDocumentEnabled: snap.ReportDocumentEnabled,
		ReportRating:          snap.ReportRating,
		RunStatus:             strings.TrimSpace(runCtx.RunStatus),
		CurrentPhase:          strings.TrimSpace(runCtx.CurrentPhase),
		Topology:              strings.TrimSpace(snap.Topology),
		RequiresIsolation:     runCtx.RequiresIsolation,
		CoordinatorBrief:      strings.TrimSpace(runCtx.CoordinatorBrief),
		FanoutPlan:            strings.TrimSpace(snap.FanoutPlan),
		FailedLeaves:          failed,
		AllowedAgents:         allowed,
		ExcludedAgents:        excluded,
		BlueprintApproval:     cloneBlueprintApprovalView(snap.BlueprintApproval),
	}
	if runCtx.PendingFeedback != nil {
		out.PendingFeedback = &PendingFeedbackView{
			PhaseID: strings.TrimSpace(runCtx.PendingFeedback.PhaseID),
			Prompt:  strings.TrimSpace(runCtx.PendingFeedback.Prompt),
		}
	}
	if runCtx.Request != nil {
		out.Request = &WorkflowRequestView{
			Status: strings.TrimSpace(runCtx.Request.Status), Source: strings.TrimSpace(runCtx.Request.Source),
			Text: strings.TrimSpace(runCtx.Request.Text), Sequence: runCtx.Request.Sequence,
		}
	}
	out.FeedbackPhases = previewFeedbackPhaseIDs(runCtx.FeedbackPhases)
	out.DecisionPhases = previewDecisionPhaseIDs(runCtx.DecisionPhases)

	current := out.CurrentPhase
	out.PhaseTotal = len(snap.Phases)
	for i, row := range snap.Phases {
		isCurrent := row.ID == current
		out.Phases = append(out.Phases, ActiveWorkflowPhaseView{
			ID:        row.ID,
			IsCurrent: isCurrent,
			Terminal:  row.Terminal,
		})
		if isCurrent {
			out.PhaseIndex = i + 1
			out.CompleteWhen = strings.TrimSpace(row.CompleteWhen)
			out.CurrentGates = actionableGateStates(row.Gates)
			// Gated phases keep only live failed leaves.
			if len(row.Gates) > 0 {
				out.FailedLeaves = liveFailedLeaves(out.FailedLeaves, out.CurrentGates)
			}
			out.NextPhase = strings.TrimSpace(row.Next)
			if out.NextPhase == "" && !row.Terminal && i+1 < len(snap.Phases) {
				out.NextPhase = snap.Phases[i+1].ID
			}
			if out.Topology != "" {
				out.TopologyPhaseID = strings.TrimSpace(row.BindTopologyStage)
			}
		}
	}
	if snap.PhaseExit != nil {
		pe := *snap.PhaseExit
		pe.VerdictSchema = maps.Clone(snap.PhaseExit.VerdictSchema)
		pe.ClaimStatuses = append([]string(nil), snap.PhaseExit.ClaimStatuses...)
		pe.OpenGates = append([]string(nil), snap.PhaseExit.OpenGates...)
		pe.DormantGates = append([]string(nil), snap.PhaseExit.DormantGates...)
		pe.ReviewAgents = append([]string(nil), snap.PhaseExit.ReviewAgents...)
		if len(snap.PhaseExit.ChoiceTransitions) > 0 {
			pe.ChoiceTransitions = make([]PhaseExitChoiceArm, len(snap.PhaseExit.ChoiceTransitions))
			for i, arm := range snap.PhaseExit.ChoiceTransitions {
				pe.ChoiceTransitions[i] = PhaseExitChoiceArm{
					ID:                 arm.ID,
					Label:              arm.Label,
					Actors:             append([]string(nil), arm.Actors...),
					CoordinatorMayFire: arm.CoordinatorMayFire,
				}
			}
		}
		out.PhaseExit = &pe
	}
	out.UnsatisfiedGateLeaves = UnsatisfiedGateIDs(out.CurrentGates)
	return out
}

func cloneBlueprintApprovalView(in *BlueprintApprovalView) *BlueprintApprovalView {
	if in == nil {
		return nil
	}
	out := *in
	return &out
}

func actionableGateStates(gates []WorkflowGateState) []WorkflowGateState {
	if len(gates) == 0 {
		return nil
	}
	out := make([]WorkflowGateState, 0, len(gates))
	for _, gate := range gates {
		if !gate.Dormant {
			out = append(out, gate)
		}
	}
	return out
}

func liveFailedLeaves(failed []string, gates []WorkflowGateState) []string {
	if len(failed) == 0 || len(gates) == 0 {
		return nil
	}
	live := make(map[string]struct{}, len(gates))
	for _, gate := range gates {
		if !gate.Satisfied {
			live[strings.TrimSpace(gate.ID)] = struct{}{}
		}
	}
	out := make([]string, 0, len(failed))
	for _, leaf := range failed {
		leaf = strings.TrimSpace(leaf)
		if _, ok := live[leaf]; ok {
			out = append(out, leaf)
		}
	}
	return out
}

// AttachGateObligations projects details for unsatisfied gates.
func AttachGateObligations(ctx context.Context, data ActiveWorkflowInjectData, catalog *feedback.GateFeedbackCatalog, advanceWhenGateMet, planContent string) ActiveWorkflowInjectData {
	if catalog == nil {
		return data
	}
	leaves := data.UnsatisfiedGateLeaves
	if len(leaves) == 0 {
		leaves = data.FailedLeaves
	}
	extras := feedback.PlanStubGateExtras(planContent)
	if data.PhaseExit != nil {
		extras = feedback.WithReviewAgents(extras, data.PhaseExit.ReviewAgents)
	}
	rows := catalog.ProjectObligations(ctx, leaves, advanceWhenGateMet, extras)
	if len(rows) == 0 {
		return data
	}
	data.GateObligations = make([]GateObligationView, len(rows))
	for i, row := range rows {
		data.GateObligations[i] = GateObligationView{
			ID:       row.ID,
			Purpose:  row.Purpose,
			Satisfy:  append([]string(nil), row.Satisfy...),
			Missing:  append([]string(nil), row.Missing...),
			Required: append([]string(nil), row.Required...),
		}
	}
	return data
}

// ActiveWorkflowInjectToMap converts the DTO for pongo2 render.
func ActiveWorkflowInjectToMap(data ActiveWorkflowInjectData, hints *guidance.HintConfig, hintCodes []string) map[string]any {
	phaseRows := make([]map[string]any, len(data.Phases))
	for i, p := range data.Phases {
		phaseRows[i] = map[string]any{
			"id":         p.ID,
			"is_current": p.IsCurrent,
			"terminal":   p.Terminal,
		}
	}
	gateRows := make([]map[string]any, len(data.CurrentGates))
	for i, g := range data.CurrentGates {
		gateRows[i] = map[string]any{"id": g.ID, "satisfied": g.Satisfied}
	}
	obligationRows := make([]map[string]any, len(data.GateObligations))
	for i, o := range data.GateObligations {
		obligationRows[i] = map[string]any{
			"id":       o.ID,
			"purpose":  o.Purpose,
			"satisfy":  append([]string(nil), o.Satisfy...),
			"missing":  append([]string(nil), o.Missing...),
			"required": append([]string(nil), o.Required...),
		}
	}
	m := map[string]any{
		"workflow_id":             data.WorkflowID,
		"workflow_version":        data.WorkflowVersion,
		"run_id":                  data.RunID,
		"run_status":              data.RunStatus,
		"report_document_enabled": data.ReportDocumentEnabled,
		"report_rating":           reportRatingRow(data.ReportRating),
		"current_phase":           data.CurrentPhase,
		"complete_when":           data.CompleteWhen,
		"topology":                data.Topology,
		"topology_phase_id":       data.TopologyPhaseID,
		"requires_isolation":      data.RequiresIsolation,
		"coordinator_brief":       data.CoordinatorBrief,
		"fanout_plan":             data.FanoutPlan,
		"failed_leaves":           data.FailedLeaves,
		"unsatisfied_gate_leaves": data.UnsatisfiedGateLeaves,
		"gate_obligations":        obligationRows,
		"allowed_agents":          data.AllowedAgents,
		"excluded_agents":         ExcludedAgentRows(data.ExcludedAgents),
		"feedback_phases":         data.FeedbackPhases,
		"decision_phases":         data.DecisionPhases,
		"phases":                  phaseRows,
		"phase_index":             data.PhaseIndex,
		"phase_total":             data.PhaseTotal,
		"next_phase":              data.NextPhase,
		"current_phase_gates":     gateRows,
	}
	if data.BlueprintApproval != nil {
		m["blueprint_approval"] = map[string]any{
			"status":        data.BlueprintApproval.Status,
			"origin":        data.BlueprintApproval.Origin,
			"parent_run_id": data.BlueprintApproval.ParentRunID,
		}
	}
	if data.Request != nil {
		m["request"] = map[string]any{
			"status": data.Request.Status, "text": data.Request.Text,
			"source": data.Request.Source, "sequence": data.Request.Sequence,
		}
	}
	if data.PhaseExit != nil {
		pe := map[string]any{
			"kind":                 data.PhaseExit.Kind,
			"depth_param":          data.PhaseExit.DepthParam,
			"coordinator_advances": data.PhaseExit.CoordinatorAdvances,
			"open_gates":           append([]string(nil), data.PhaseExit.OpenGates...),
			"dormant_gates":        append([]string(nil), data.PhaseExit.DormantGates...),
			"complete_when":        data.PhaseExit.CompleteWhen,
			"verdict_schema":       verdictSchemaJSON(data.PhaseExit.VerdictSchema),
			"claim_statuses":       append([]string(nil), data.PhaseExit.ClaimStatuses...),
			"review_loop_key":      data.PhaseExit.ReviewLoopKey,
			"review_loop_cap":      data.PhaseExit.ReviewLoopCap,
			"review_agents":        append([]string(nil), data.PhaseExit.ReviewAgents...),
			"human_approval":       data.PhaseExit.HumanApproval,
			"invoke_workflow_id":   data.PhaseExit.InvokeWorkflowID,
		}
		if len(data.PhaseExit.ChoiceTransitions) > 0 {
			arms := make([]map[string]any, 0, len(data.PhaseExit.ChoiceTransitions))
			for _, arm := range data.PhaseExit.ChoiceTransitions {
				arms = append(arms, map[string]any{
					"id":                   arm.ID,
					"label":                arm.Label,
					"actors":               append([]string(nil), arm.Actors...),
					"coordinator_may_fire": arm.CoordinatorMayFire,
				})
			}
			pe["choice_transitions"] = arms
		}
		m["phase_exit"] = pe
	}
	if data.PendingFeedback != nil {
		m["pending_feedback"] = map[string]any{
			"phase_id": data.PendingFeedback.PhaseID,
			"prompt":   data.PendingFeedback.Prompt,
		}
	}
	if hints != nil && len(hintCodes) > 0 {
		m["workflow_hints"] = workflowHintViews(context.Background(), hints, hintCodes)
	}
	return m
}

func workflowHintViews(ctx context.Context, hints *guidance.HintConfig, codes []string) []map[string]any {
	views := make([]map[string]any, 0, len(codes))
	for _, code := range codes {
		entry, ok := hints.HintCodes[code]
		if !ok {
			continue
		}
		msg, _, fix := guidance.RenderHintFields(code, entry, nil)
		if msg == "" {
			msg = strings.TrimSpace(entry.Message)
		}
		if fix == "" {
			fix = strings.TrimSpace(entry.Fix)
		}
		views = append(views, map[string]any{
			"code":    code,
			"message": msg,
			"fix":     fix,
		})
	}
	return views
}

func previewFeedbackPhaseIDs(phases []api.ComposeFeedbackPhase) []string {
	if len(phases) == 0 {
		return nil
	}
	limit := len(phases)
	if limit > maxPhaseIDPreview {
		limit = maxPhaseIDPreview
	}
	out := make([]string, 0, limit)
	for i := 0; i < limit; i++ {
		out = append(out, phases[i].ID)
	}
	return out
}

func previewDecisionPhaseIDs(phases []api.ComposeDecisionPhase) []string {
	if len(phases) == 0 {
		return nil
	}
	limit := len(phases)
	if limit > maxPhaseIDPreview {
		limit = maxPhaseIDPreview
	}
	out := make([]string, 0, limit)
	for i := 0; i < limit; i++ {
		out = append(out, phases[i].ID)
	}
	return out
}

// String maps always marshal; encoding/json sorts keys for stable render/cache identity.
func verdictSchemaJSON(schema map[string]string) string {
	if len(schema) == 0 {
		return ""
	}
	raw, err := json.Marshal(schema)
	if err != nil {
		panic(fmt.Errorf("encode string-only verdict schema: %w", err))
	}
	return string(raw)
}

func reportRatingRow(r *ReportRatingView) map[string]any {
	if r == nil {
		return nil
	}
	return map[string]any{"dimensions": append([]string(nil), r.Dimensions...), "questions": r.Questions}
}
