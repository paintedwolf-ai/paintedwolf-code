package definition

import (
	"fmt"
	"github.com/lycaon/lycaon/internal/blueprint"
	sessionposture "github.com/lycaon/lycaon/internal/session/posture"
	"slices"
	"strings"
)

func parsePhaseYAML(p phaseYAML) (PhaseDef, error) {
	id := strings.TrimSpace(p.ID)
	if id == "" {
		return PhaseDef{}, nil
	}
	if !manifestIdentifier.MatchString(id) {
		return PhaseDef{}, fmt.Errorf("phase id %q must contain only lowercase letters, digits, underscores, or hyphens", id)
	}
	activityLabel := strings.TrimSpace(p.ActivityLabel)
	if activityLabel == "" {
		return PhaseDef{}, fmt.Errorf("phase %q: activity_label required", id)
	}
	def := PhaseDef{
		ID:                 id,
		ActivityLabel:      activityLabel,
		CompleteWhen:       strings.TrimSpace(p.CompleteWhen),
		EntryWhen:          strings.TrimSpace(p.EntryWhen),
		InvokeTrigger:      InvokeTrigger(strings.TrimSpace(p.InvokeTrigger)),
		Next:               strings.TrimSpace(p.Next),
		ChildNext:          strings.TrimSpace(p.ChildNext),
		ChildCompleteWhen:  strings.TrimSpace(p.ChildCompleteWhen),
		ChildGates:         append([]string(nil), p.ChildGates...),
		Gates:              append([]string(nil), p.Gates...),
		BindTopologyStage:  strings.TrimSpace(p.BindTopologyStage),
		BindParallelGroup:  append([]string(nil), p.BindParallelGroup...),
		CoordinatorSurface: strings.TrimSpace(p.CoordinatorSurface),
		SurfaceTemplate:    strings.TrimSpace(p.SurfaceTemplate),
		BlueprintWrite:     p.BlueprintWrite,
	}
	if len(p.ModeRefs) > 0 {
		def.ModeRefs = make([]string, 0, len(p.ModeRefs))
		for _, ref := range p.ModeRefs {
			ref = strings.TrimSpace(ref)
			if ref == "" {
				return PhaseDef{}, fmt.Errorf("phase %q: empty mode_refs entry", id)
			}
			def.ModeRefs = append(def.ModeRefs, ref)
		}
	}
	if p.InvokeWorkflow != nil {
		var err error
		def.InvokeWorkflow, err = parseInvokeWorkflowYAML(id, *p.InvokeWorkflow)
		if err != nil {
			return PhaseDef{}, err
		}
	}
	if p.OnEnter != nil {
		enter, err := parseOnEnterYAML(id, *p.OnEnter)
		if err != nil {
			return PhaseDef{}, err
		}
		def.OnEnter = enter
	}
	if p.OnReenter != nil {
		reenter, err := parseOnReenterYAML(id, *p.OnReenter)
		if err != nil {
			return PhaseDef{}, err
		}
		def.OnReenter = reenter
	}
	if p.ParallelTask != nil {
		def.ParallelTask = &ParallelTask{
			MaxWorkers:      p.ParallelTask.MaxWorkers,
			MaxReadWorkers:  p.ParallelTask.MaxReadWorkers,
			MaxWriteWorkers: p.ParallelTask.MaxWriteWorkers,
		}
	}
	if p.Fanout != nil {
		def.Fanout = FanoutOptions{RequireThreatModel: p.Fanout.RequireThreatModel, RequireTaskCharter: p.Fanout.RequireTaskCharter, MaxAttempts: p.Fanout.MaxAttempts}
	}
	if p.Touch != nil && len(p.Touch.Paths) > 0 {
		def.TouchPaths = append([]string(nil), p.Touch.Paths...)
	}
	def.Terminal = p.Terminal
	if p.Advance != nil {
		wgm, err := parseAdvanceWhenGateMet(id, p.Advance.WhenGateMet)
		if err != nil {
			return PhaseDef{}, err
		}
		def.AdvanceWhenGateMet = wgm
	}
	if p.Loop != nil {
		exit, err := parseLoopExit(id, p.Loop.Exit)
		if err != nil {
			return PhaseDef{}, err
		}
		def.LoopExit = exit
	}
	if p.Controls != nil {
		if p.Controls.ContentReview != nil {
			def.ContentReview = &PhaseContentReview{
				Tools: append([]string(nil), p.Controls.ContentReview.Tools...),
				Paths: append([]string(nil), p.Controls.ContentReview.Paths...),
			}
		}
		if c := strings.TrimSpace(p.Controls.Closeout); c != "" {
			if CloseoutPolicy(c) != CloseoutGated {
				return PhaseDef{}, fmt.Errorf("phase %q: invalid controls.closeout %q (want gated)", id, c)
			}
			def.Closeout = CloseoutGated
		}
		if p.Controls.Retries != nil {
			if *p.Controls.Retries <= 0 {
				return PhaseDef{}, fmt.Errorf("phase %q: controls.retries must be positive", id)
			}
			def.CloseoutRetries = *p.Controls.Retries
		}
	}
	if len(p.Intake) > 0 {
		def.Intake = make([]string, 0, len(p.Intake))
		for _, key := range p.Intake {
			key = strings.TrimSpace(key)
			if key == "" {
				return PhaseDef{}, fmt.Errorf("phase %q: empty intake key", id)
			}
			def.Intake = append(def.Intake, key)
		}
	}
	if p.HumanApproval != nil {
		cfg, err := parseHumanApprovalYAML(id, *p.HumanApproval)
		if err != nil {
			return PhaseDef{}, err
		}
		def.HumanApproval = cfg
	}
	if p.ReviewLoop != nil {
		rl, err := parseReviewLoopYAML(id, *p.ReviewLoop)
		if err != nil {
			return PhaseDef{}, err
		}
		def.ReviewLoop = rl
	}
	def.DepthParam = strings.TrimSpace(p.DepthParam)
	if p.Explain != nil {
		def.Explain = &PhaseExplain{
			Summary: strings.TrimSpace(p.Explain.Summary),
			Body:    strings.TrimSpace(p.Explain.Body),
		}
	}
	if len(p.Transitions) > 0 {
		transitions, err := parsePhaseTransitionsYAML(id, p.Transitions)
		if err != nil {
			return PhaseDef{}, err
		}
		def.Transitions = transitions
	}
	for i, g := range def.Gates {
		def.Gates[i] = strings.TrimSpace(g)
		if def.Gates[i] == "" {
			return PhaseDef{}, fmt.Errorf("phase %q: empty gates entry", id)
		}
	}
	for i, gate := range def.ChildGates {
		def.ChildGates[i] = strings.TrimSpace(gate)
		if def.ChildGates[i] == "" {
			return PhaseDef{}, fmt.Errorf("phase %q: empty child_gates entry", id)
		}
	}
	return def, nil
}

func parseInvokeWorkflowYAML(phaseID string, raw invokeWorkflowYAML) (*InvokeWorkflowSpec, error) {
	wid := strings.TrimSpace(raw.WorkflowID)
	ver := strings.TrimSpace(raw.Version)
	blueprintMode := ChildBlueprintMode(strings.TrimSpace(raw.Blueprint))
	if wid == "" {
		return nil, fmt.Errorf("phase %q: invoke_workflow.workflow_id required", phaseID)
	}
	if ver == "" {
		return nil, fmt.Errorf("phase %q: invoke_workflow.version required", phaseID)
	}
	switch blueprintMode {
	case ChildBlueprintNone, ChildBlueprintInherit, ChildBlueprintOwn:
	default:
		return nil, fmt.Errorf("phase %q: invoke_workflow.blueprint must be one of none, inherit, own", phaseID)
	}
	return &InvokeWorkflowSpec{WorkflowID: wid, Version: ver, Blueprint: blueprintMode}, nil
}

func parsePhaseTransitionsYAML(phaseID string, raw []transitionYAML) ([]PhaseTransitionDef, error) {
	out := make([]PhaseTransitionDef, 0, len(raw))
	seen := map[string]struct{}{}
	for i, t := range raw {
		id := strings.TrimSpace(t.ID)
		to := strings.TrimSpace(t.To)
		label := strings.TrimSpace(t.Label)
		when := strings.TrimSpace(t.When)
		if id == "" {
			return nil, fmt.Errorf("phase %q: transitions[%d]: id required", phaseID, i)
		}
		if _, dup := seen[id]; dup {
			return nil, fmt.Errorf("phase %q: duplicate transition id %q", phaseID, id)
		}
		seen[id] = struct{}{}
		if to == "" {
			return nil, fmt.Errorf("phase %q: transition %q: to required", phaseID, id)
		}
		if to == phaseID {
			return nil, fmt.Errorf("phase %q: transition %q: self-edge forbidden in v1", phaseID, id)
		}
		if label == "" {
			return nil, fmt.Errorf("phase %q: transition %q: label required", phaseID, id)
		}
		if len(t.Actors) == 0 {
			return nil, fmt.Errorf("phase %q: transition %q: actors required", phaseID, id)
		}
		actors := make([]string, 0, len(t.Actors))
		seenActor := map[string]struct{}{}
		for _, a := range t.Actors {
			a = strings.TrimSpace(a)
			switch a {
			case TransitionActorHuman, TransitionActorCoordinator:
			default:
				return nil, fmt.Errorf("phase %q: transition %q: invalid actor %q (want human|coordinator)", phaseID, id, a)
			}
			if _, ok := seenActor[a]; ok {
				continue
			}
			seenActor[a] = struct{}{}
			actors = append(actors, a)
		}
		out = append(out, PhaseTransitionDef{
			ID:     id,
			To:     to,
			Actors: actors,
			Label:  label,
			When:   when,
		})
	}
	return out, nil
}

func parseHumanApprovalYAML(phaseID string, raw humanApprovalYAML) (*HumanApprovalConfig, error) {
	blueprint := blueprint.ConventionPath(raw.Blueprint)
	return &HumanApprovalConfig{
		Blueprint: blueprint,
		Readiness: strings.TrimSpace(raw.Readiness),
	}, nil
}

func parseReviewLoopYAML(phaseID string, raw reviewLoopYAML) (*ReviewLoopDef, error) {
	key := strings.TrimSpace(raw.EvidenceKey)
	if key == "" {
		return nil, fmt.Errorf("phase %q: review_loop.evidence_key required", phaseID)
	}
	cap := raw.IterationCap
	if raw.FollowupAttempts > 0 {
		if cap != 0 {
			return nil, fmt.Errorf("phase %q: followup_attempts replaces iteration_cap", phaseID)
		}
	} else if cap <= 0 {
		cap = 3
	}
	agents := uniqueAgentIDs(raw.RequiredAgents)
	spawnable := uniqueAgentIDs(raw.IfSpawnable)
	def := &ReviewLoopDef{
		CoverageReviewers:         uniqueAgentIDs(raw.CoverageReviewers),
		ReconcilesPhase:           raw.ReconcilesPhase,
		FollowupAttempts:          raw.FollowupAttempts,
		RequireInventoryAccounted: raw.RequireInventoryAccounted,
		IncludeScanInventory:      raw.IncludeScanInventory,
		EvidenceKey:               key,
		IterationCap:              cap,
		VerdictSchema:             copyStringMap(raw.VerdictSchema),
		RequiredAgents:            agents,
		IfSpawnable:               spawnable,
		BriefLabel:                strings.TrimSpace(raw.BriefLabel),
	}
	statuses, err := parseClaimStatuses(phaseID, raw.ClaimStatuses)
	if err != nil {
		return nil, err
	}
	coverageFields := 0
	for _, kind := range def.VerdictSchema {
		if kind == VerdictCoverageType {
			coverageFields++
		}
	}
	if coverageFields > 1 {
		return nil, fmt.Errorf("phase %q: review_loop declares multiple coverage reviews", phaseID)
	}
	if def.FollowupAttempts < 0 || def.FollowupAttempts > 4 {
		return nil, fmt.Errorf("phase %q: followup_attempts must be between 0 and 4", phaseID)
	}
	if def.FollowupAttempts > 0 && (!def.CarriesClaims() || !def.CarriesCoverage() || def.ReconcilesPhase == "" || len(agents) == 0 || len(strings.Split(def.VerdictSchema[VerdictDecisionKey], "|")) < 2) {
		return nil, fmt.Errorf("phase %q: follow-up requires reconciled claims, coverage, a reviewer, and a non-terminal verdict", phaseID)
	}
	for _, agent := range def.CoverageReviewers {
		if !slices.Contains(agents, agent) || !def.CarriesCoverage() || def.ReconcilesPhase == "" {
			return nil, fmt.Errorf("phase %q: coverage_reviewers requires coverage, reconciles_phase, and required_agents membership", phaseID)
		}
	}
	def.ClaimStatuses = statuses
	if def.CarriesClaims() && len(statuses) == 0 {
		return nil, fmt.Errorf("phase %q: review_loop carries claims and must declare claim_statuses", phaseID)
	}
	if !def.CarriesClaims() && len(statuses) > 0 {
		return nil, fmt.Errorf("phase %q: review_loop declares claim_statuses but its verdict_schema carries no claims", phaseID)
	}
	if def.BriefLabel != "" && !def.CarriesClaims() {
		return nil, fmt.Errorf("phase %q: review_loop.brief_label names a check of claims, but the schema carries none", phaseID)
	}
	return def, nil
}

// parseObligationsYAML checks names and uniqueness.
func parseObligationsYAML(phaseID string, raw []obligationYAML) ([]ObligationDef, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	out := make([]ObligationDef, 0, len(raw))
	seen := map[string]struct{}{}
	for i, ob := range raw {
		kind := strings.TrimSpace(ob.Kind)
		if kind == "" {
			return nil, fmt.Errorf("phase %q: on_enter.obligations[%d]: kind required", phaseID, i)
		}
		if _, dup := seen[kind]; dup {
			return nil, fmt.Errorf("phase %q: duplicate obligation kind %q", phaseID, kind)
		}
		seen[kind] = struct{}{}
		def := ObligationDef{Kind: kind}
		if len(ob.Params) > 0 {
			params := make(map[string]any, len(ob.Params))
			for k, v := range ob.Params {
				params[k] = v
			}
			def.Params = params
		}
		out = append(out, def)
	}
	return out, nil
}

func parseOnEnterYAML(phaseID string, raw onEnterYAML) (PhaseOnEnter, error) {
	var enter PhaseOnEnter
	if sm := strings.TrimSpace(raw.SetPosture); sm != "" {
		if !sessionposture.ValidSessionPosture(sm) {
			return PhaseOnEnter{}, fmt.Errorf("phase %q: invalid set_posture %q", phaseID, sm)
		}
		enter.SetPosture = sm
	}
	if raw.RequestUserFeedback != nil {
		prompt := strings.TrimSpace(raw.RequestUserFeedback.Prompt)
		if prompt == "" {
			return PhaseOnEnter{}, fmt.Errorf("phase %q: request_user_feedback.prompt required", phaseID)
		}
		responseType := FeedbackResponseType(strings.TrimSpace(raw.RequestUserFeedback.ResponseType))
		if responseType == "" {
			responseType = FeedbackResponseText
		}
		switch responseType {
		case FeedbackResponseText, FeedbackResponseSingleChoice, FeedbackResponseMultiChoice:
		default:
			return PhaseOnEnter{}, fmt.Errorf("phase %q: invalid request_user_feedback.response_type %q", phaseID, responseType)
		}
		opts := make([]string, 0, len(raw.RequestUserFeedback.Options))
		for _, o := range raw.RequestUserFeedback.Options {
			o = strings.TrimSpace(o)
			if o != "" {
				opts = append(opts, o)
			}
		}
		if responseType.IsChoice() && len(opts) < 2 {
			return PhaseOnEnter{}, fmt.Errorf("phase %q: request_user_feedback.options requires at least 2 choices for %s", phaseID, responseType)
		}
		if !responseType.IsChoice() && len(opts) > 0 {
			return PhaseOnEnter{}, fmt.Errorf("phase %q: request_user_feedback.options only valid with a choice response_type", phaseID)
		}
		enter.RequestUserFeedback = &UserFeedbackPrompt{
			Prompt:       prompt,
			ResponseType: responseType,
			Options:      opts,
			AllowOther:   raw.RequestUserFeedback.AllowOther,
		}
	}
	if raw.PromptCoordinator {
		enter.PromptCoordinator = true
	}
	if sem := strings.TrimSpace(raw.SetExecutionMode); sem != "" {
		if err := ValidateExecutionModeField("on_enter.set_execution_mode", sem); err != nil {
			return PhaseOnEnter{}, fmt.Errorf("phase %q: %w", phaseID, err)
		}
		enter.SetExecutionMode = NormalizeExecutionMode(sem)
	}
	obligations, err := parseObligationsYAML(phaseID, raw.Obligations)
	if err != nil {
		return PhaseOnEnter{}, err
	}
	enter.Obligations = obligations
	return enter, nil
}

func parseOnReenterYAML(phaseID string, raw onReenterYAML) (PhaseOnReenter, error) {
	var reenter PhaseOnReenter
	if kick := strings.TrimSpace(raw.InjectKick); kick != "" {
		reenter.InjectKick = kick
	}
	if leg := strings.TrimSpace(raw.ReenterLeg); leg != "" {
		reenter.ReenterLeg = leg
	}
	if reenter.IsZero() {
		return PhaseOnReenter{}, fmt.Errorf("phase %q: on_reenter requires inject_kick and/or reenter_leg", phaseID)
	}
	return reenter, nil
}
