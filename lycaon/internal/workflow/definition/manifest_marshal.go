package definition

import (
	"path/filepath"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/blueprint"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/sandbox"
	"gopkg.in/yaml.v3"
)

// MarshalManifestYAML serializes an effective manifest for session storage.
func MarshalManifestYAML(m Manifest) (string, error) {
	wf := manifestToWorkflowFile(m)
	raw, err := yaml.Marshal(wf)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func manifestToWorkflowFile(m Manifest) workflowFile {
	wf := workflowFile{
		ID:      m.ID,
		Version: m.Version,
		Retired: m.Retired,
		Attach:  manifestAttachYAML(m.Attach),
		Request: manifestRequestYAML(m.Request),
		// Session storage persists the resolved manifest.
		Extends:            "",
		Name:               m.Name,
		Description:        m.Description,
		Trigger:            m.Trigger,
		InitialPosture:     m.InitialPosture,
		Icon:               m.Icon,
		Featured:           m.Featured,
		RequiresRepo:       m.RequiresRepo,
		CoordinatorProfile: m.CoordinatorProfile,
		SurfaceProfile:     m.SurfaceProfile,
		Agents:             manifestAgentYAML(m),
		Gates:              append([]string(nil), m.Gates...),
		Rules:              append([]string(nil), m.Rules...),
		Topology:           m.Topology,
		Controls:           manifestControlsYAML(m.Controls),
		Parameters:         manifestParametersYAML(m.Parameters),
		Blueprint:          manifestBlueprintYAML(m.Blueprint),
		Presets:            manifestPresetsYAML(m.Presets),
		Injects:            append([]anchor.WorkflowInject(nil), m.Injects...),
	}
	for _, p := range m.PhaseDefs {
		wf.Phases = append(wf.Phases, phaseToYAML(p))
	}
	return wf
}

func manifestAttachYAML(attach ManifestAttach) *attachYAML {
	if attach.Policy == "" {
		return nil
	}
	return &attachYAML{Policy: string(attach.Policy)}
}

func manifestRequestYAML(request *ManifestRequest) *requestYAML {
	if request == nil {
		return nil
	}
	return &requestYAML{
		Cadence:  string(request.Cadence),
		Question: request.Question,
		Default:  request.Default,
	}
}

func manifestControlsYAML(controls ManifestControls) workflowControls {
	out := workflowControls{
		PhaseAdvance:         string(controls.PhaseAdvance),
		DefaultExecutionMode: controls.DefaultExecutionMode,
	}
	if controls.OnDecisionReject != nil {
		out.OnDecisionReject = &decisionRejectYAML{Pause: controls.OnDecisionReject.Pause, Cancel: controls.OnDecisionReject.Cancel}
	}
	if controls.OnPause != nil {
		out.OnPause = &pauseControlsYAML{HoldPending: controls.OnPause.HoldPending, CancelRunning: controls.OnPause.CancelRunning}
	}
	if controls.OnStop != nil {
		out.OnStop = &stopControlsYAML{
			CancelWorkers: controls.OnStop.CancelWorkers, AbortDelegation: controls.OnStop.AbortDelegation,
			SessionAbort: controls.OnStop.SessionAbort,
		}
	}
	if controls.ContentReview != nil {
		out.ContentReview = contentReviewToYAML(controls.ContentReview)
	}
	if controls.Report != nil {
		out.Report = &reportControlsYAML{
			Enabled:       controls.Report.Enabled,
			FindingsLabel: controls.Report.FindingsLabel,
			Brief:         controls.Report.Brief.toYAML(),
		}
		if controls.Report.Retries > 0 {
			r := controls.Report.Retries
			out.Report.Retries = &r
		}
	}
	return out
}

func manifestParametersYAML(params map[string]WorkflowParameter) map[string]workflowParameterYAML {
	if len(params) == 0 {
		return nil
	}
	out := make(map[string]workflowParameterYAML, len(params))
	for name, spec := range params {
		out[name] = workflowParameterYAML(spec)
	}
	return out
}

func manifestBlueprintYAML(def *BlueprintDef) *blueprintYAML {
	if def == nil {
		return nil
	}
	file := strings.TrimPrefix(filepath.ToSlash(def.Path), blueprint.BlueprintsDir()+"/")
	return &blueprintYAML{ID: def.ID, File: file, Frontmatter: append([]string(nil), def.Frontmatter...)}
}

func manifestPresetsYAML(presets []ManifestPreset) []manifestPresetYAML {
	out := make([]manifestPresetYAML, 0, len(presets))
	for _, preset := range presets {
		params := make(map[string]string, len(preset.Params))
		for name, value := range preset.Params {
			params[name] = value
		}
		out = append(out, manifestPresetYAML{
			ID: preset.ID, Name: preset.Name, Description: preset.Description, Trigger: preset.Trigger, Params: params,
		})
	}
	return out
}

func phaseToYAML(p PhaseDef) phaseYAML {
	out := phaseYAML{
		ID: p.ID, ActivityLabel: p.ActivityLabel, CompleteWhen: p.CompleteWhen, EntryWhen: p.EntryWhen,
		InvokeTrigger: string(p.InvokeTrigger), Next: p.Next, ChildNext: p.ChildNext,
		ChildCompleteWhen: p.ChildCompleteWhen, ChildGates: append([]string(nil), p.ChildGates...),
		Gates: append([]string(nil), p.Gates...), BindTopologyStage: p.BindTopologyStage,
		BindParallelGroup: append([]string(nil), p.BindParallelGroup...), Terminal: p.Terminal,
		CoordinatorSurface: p.CoordinatorSurface, SurfaceTemplate: p.SurfaceTemplate,
		ModeRefs: append([]string(nil), p.ModeRefs...), Intake: append([]string(nil), p.Intake...), DepthParam: p.DepthParam,
		BlueprintWrite: p.BlueprintWrite,
	}
	if p.InvokeWorkflow != nil {
		out.InvokeWorkflow = &invokeWorkflowYAML{
			WorkflowID: p.InvokeWorkflow.WorkflowID, Version: p.InvokeWorkflow.Version,
			Blueprint: string(p.InvokeWorkflow.Blueprint),
		}
	}
	if p.Explain != nil {
		out.Explain = &explainYAML{Summary: p.Explain.Summary, Body: p.Explain.Body}
	}
	out.OnEnter = phaseOnEnterYAML(p.OnEnter)
	if !p.OnReenter.IsZero() {
		out.OnReenter = &onReenterYAML{InjectKick: p.OnReenter.InjectKick, ReenterLeg: p.OnReenter.ReenterLeg}
	}
	if p.ContentReview != nil || p.Closeout != "" || p.CloseoutRetries > 0 {
		out.Controls = &phaseControlsYAML{ContentReview: contentReviewToYAML(p.ContentReview), Closeout: string(p.Closeout)}
		if p.CloseoutRetries > 0 {
			r := p.CloseoutRetries
			out.Controls.Retries = &r
		}
	}
	if p.ParallelTask != nil {
		out.ParallelTask = &parallelTaskYAML{
			MaxWorkers: p.ParallelTask.MaxWorkers, MaxReadWorkers: p.ParallelTask.MaxReadWorkers,
			MaxWriteWorkers: p.ParallelTask.MaxWriteWorkers,
		}
	}
	if p.Fanout.RequireThreatModel || p.Fanout.RequireTaskCharter || p.Fanout.MaxAttempts > 0 {
		out.Fanout = &fanoutYAML{RequireThreatModel: p.Fanout.RequireThreatModel, RequireTaskCharter: p.Fanout.RequireTaskCharter, MaxAttempts: p.Fanout.MaxAttempts}
	}
	if len(p.TouchPaths) > 0 {
		out.Touch = &phaseTouchYAML{Paths: append([]string(nil), p.TouchPaths...)}
	}
	if p.AdvanceWhenGateMet != "" {
		out.Advance = &phaseAdvanceYAML{WhenGateMet: string(p.AdvanceWhenGateMet)}
	}
	if p.LoopExit != "" {
		out.Loop = &phaseLoopYAML{Exit: string(p.LoopExit)}
	}
	if p.HumanApproval != nil {
		path := strings.TrimPrefix(filepath.ToSlash(p.HumanApproval.Blueprint), blueprint.BlueprintsDir()+"/")
		out.HumanApproval = &humanApprovalYAML{Blueprint: path, Readiness: p.HumanApproval.Readiness}
	}
	if p.ReviewLoop != nil {
		out.ReviewLoop = &reviewLoopYAML{
			ReconcilesPhase:           p.ReviewLoop.ReconcilesPhase,
			CoverageReviewers:         append([]string(nil), p.ReviewLoop.CoverageReviewers...),
			FollowupAttempts:          p.ReviewLoop.FollowupAttempts,
			RequireInventoryAccounted: p.ReviewLoop.RequireInventoryAccounted,
			IncludeScanInventory:      p.ReviewLoop.IncludeScanInventory,
			EvidenceKey:               p.ReviewLoop.EvidenceKey, IterationCap: p.ReviewLoop.IterationCap,
			VerdictSchema:  copyStringMap(p.ReviewLoop.VerdictSchema),
			RequiredAgents: append([]string(nil), p.ReviewLoop.RequiredAgents...),
			IfSpawnable:    append([]string(nil), p.ReviewLoop.IfSpawnable...),
			ClaimStatuses:  claimStatusesYAML(p.ReviewLoop.ClaimStatuses),
			BriefLabel:     p.ReviewLoop.BriefLabel,
		}
	}
	for _, transition := range p.Transitions {
		out.Transitions = append(out.Transitions, transitionYAML{
			ID: transition.ID, To: transition.To, Actors: append([]string(nil), transition.Actors...),
			Label: transition.Label, When: transition.When,
		})
	}
	return out
}

func phaseOnEnterYAML(enter PhaseOnEnter) *onEnterYAML {
	if enter.IsZero() {
		return nil
	}
	out := &onEnterYAML{
		SetPosture: enter.SetPosture, SetExecutionMode: enter.SetExecutionMode,
		PromptCoordinator: enter.PromptCoordinator,
	}
	if prompt := enter.RequestUserFeedback; prompt != nil {
		out.RequestUserFeedback = &userFeedbackYAML{
			Prompt: prompt.Prompt, ResponseType: string(prompt.ResolvedResponseType()),
			Options: append([]string(nil), prompt.Options...), AllowOther: prompt.AllowOther,
		}
	}
	for _, obligation := range enter.Obligations {
		params := make(map[string]any, len(obligation.Params))
		for name, value := range obligation.Params {
			params[name] = value
		}
		out.Obligations = append(out.Obligations, obligationYAML{Kind: obligation.Kind, Params: params})
	}
	return out
}

func contentReviewToYAML(review *PhaseContentReview) *contentReviewYAML {
	if review == nil {
		return nil
	}
	return &contentReviewYAML{Tools: append([]string(nil), review.Tools...), Paths: append([]string(nil), review.Paths...)}
}

func manifestAgentYAML(m Manifest) []agentYAML {
	if len(m.AgentToolAccess) == 0 {
		return nil
	}
	out := make([]agentYAML, 0, len(m.AgentToolAccess))
	emitted := make(map[string]bool, len(m.AllowedAgents))
	for _, id := range m.AllowedAgents {
		access := m.AgentToolAccess[id]
		if access == "" {
			access = sandbox.ToolAccessProfile
		}
		out = append(out, agentYAML{ID: id, Tools: access})
		emitted[id] = true
	}
	extra := make([]string, 0, len(m.AgentToolAccess)-len(emitted))
	for id := range m.AgentToolAccess {
		if !emitted[id] {
			extra = append(extra, id)
		}
	}
	sort.Strings(extra)
	spawn := false
	for _, id := range extra {
		out = append(out, agentYAML{ID: id, Tools: m.AgentToolAccess[id], Spawn: &spawn})
	}
	return out
}
