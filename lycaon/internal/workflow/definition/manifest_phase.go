package definition

import (
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
)

const maxExtendsDepth = 3

// FeedbackResponseType selects how a request_user_feedback phase collects its answer.
type FeedbackResponseType string

const (
	// FeedbackResponseText is open-ended freeform input (the default).
	FeedbackResponseText FeedbackResponseType = "text"
	// FeedbackResponseSingleChoice is one option from Options (radio).
	FeedbackResponseSingleChoice FeedbackResponseType = "single_choice"
	// FeedbackResponseMultiChoice is any number of options from Options (checkbox).
	FeedbackResponseMultiChoice FeedbackResponseType = "multi_choice"
	// FeedbackResponseSecret collects a value through the protected secret path.
	// It is valid for coordinator ask_user only, never manifest feedback.
	FeedbackResponseSecret FeedbackResponseType = "secret"
)

// IsChoice reports whether the response is one of the structured-option types.
func (t FeedbackResponseType) IsChoice() bool {
	return t == FeedbackResponseSingleChoice || t == FeedbackResponseMultiChoice
}

// UserFeedbackPrompt defines a phase-entry inquiry.
type UserFeedbackPrompt struct {
	Prompt       string
	ResponseType FeedbackResponseType
	Options      []string
	AllowOther   bool
	ArtifactID   string
	ArtifactIDs  []string
	Purpose      string
	Secret       *SecretInputSpec
}

// ResolvedResponseType defaults an empty ResponseType to text.
func (p UserFeedbackPrompt) ResolvedResponseType() FeedbackResponseType {
	if p.ResponseType == "" {
		return FeedbackResponseText
	}
	return p.ResponseType
}

// PhaseOnEnter hooks run on cross-phase entry.
type PhaseOnEnter struct {
	SetPosture          string
	SetExecutionMode    string
	PromptCoordinator   bool
	RequestUserFeedback *UserFeedbackPrompt
	// Obligations hold the phase until their gates settle.
	Obligations []ObligationDef
}

func (e PhaseOnEnter) IsZero() bool {
	return e.SetPosture == "" && e.SetExecutionMode == "" && !e.PromptCoordinator &&
		e.RequestUserFeedback == nil && len(e.Obligations) == 0
}

// PhaseOnReenter hooks run on same-phase auto-advance.
type PhaseOnReenter struct {
	InjectKick string
	ReenterLeg string
}

func (e PhaseOnReenter) IsZero() bool {
	return e.InjectKick == "" && e.ReenterLeg == ""
}

// ParallelTask caps opportunistic coordinator task() fan-out for a phase.
type ParallelTask struct {
	MaxWorkers      int
	MaxReadWorkers  int
	MaxWriteWorkers int
}

// FanoutOptions is phase-level policy for fanout_plan.
type FanoutOptions struct {
	RequireThreatModel bool
	RequireTaskCharter bool
	MaxAttempts        int
}

// InvokeTrigger selects when the host starts a child workflow run.
type InvokeTrigger string

const (
	InvokeTriggerPhaseEnter      InvokeTrigger = "phase_enter"
	InvokeTriggerCoordinatorTool InvokeTrigger = "coordinator_tool"
)

// ChildBlueprintMode defines which governing document an invoked workflow uses.
type ChildBlueprintMode string

const (
	// ChildBlueprintNone starts the child without a governing Blueprint.
	ChildBlueprintNone ChildBlueprintMode = "none"
	// ChildBlueprintInherit binds the child to the parent's approved Blueprint.
	ChildBlueprintInherit ChildBlueprintMode = "inherit"
	// ChildBlueprintOwn creates a governing Blueprint for the child.
	ChildBlueprintOwn ChildBlueprintMode = "own"
)

// InvokeWorkflowSpec names a child manifest to run as a subroutine.
type InvokeWorkflowSpec struct {
	WorkflowID string
	Version    string
	Blueprint  ChildBlueprintMode
}

// BlueprintDef declares a workflow governing document for runtime inject.
type BlueprintDef struct {
	ID          string
	Path        string
	Frontmatter []string
}

// ReviewLoopDef defines a bounded parallel review cycle.
type ReviewLoopDef struct {
	CoverageReviewers         []string
	FollowupAttempts          int
	ReconcilesPhase           string
	RequireInventoryAccounted bool
	IncludeScanInventory      bool
	EvidenceKey               string
	IterationCap              int
	VerdictSchema             map[string]string
	RequiredAgents            []string
	// IfSpawnable are reviewers required only when the turn roster can spawn them
	// at phase enter. The host snapshots that roster; a later Settings flip does
	// not change the sojourn.
	IfSpawnable []string
	// ClaimStatuses maps each status word this phase may give a claim to the
	// class it settles the claim into. Required when the schema carries claims.
	ClaimStatuses map[string]ClaimClass
	// BriefLabel names this review for a reader who never saw the workflow,
	// such as "Second opinion". A phase without one is not listed as a check.
	BriefLabel string
}

// PhaseDef is a fully-resolved workflow phase.
type PhaseDef struct {
	ID                 string
	ActivityLabel      string
	CompleteWhen       string
	EntryWhen          string
	InvokeWorkflow     *InvokeWorkflowSpec
	InvokeTrigger      InvokeTrigger
	Next               string
	ChildNext          string
	ChildCompleteWhen  string
	ChildGates         []string
	OnEnter            PhaseOnEnter
	OnReenter          PhaseOnReenter
	Gates              []string
	BindTopologyStage  string
	BindParallelGroup  []string
	ContentReview      *PhaseContentReview
	Closeout           CloseoutPolicy
	CloseoutRetries    int
	ParallelTask       *ParallelTask
	Fanout             FanoutOptions
	TouchPaths         []string
	Terminal           bool
	CoordinatorSurface string
	SurfaceTemplate    string
	ModeRefs           []string
	AdvanceWhenGateMet AdvanceWhenGateMet
	LoopExit           LoopExit
	Intake             []string
	HumanApproval      *HumanApprovalConfig
	ReviewLoop         *ReviewLoopDef
	DepthParam         string
	Transitions        []PhaseTransitionDef
	// BlueprintWrite declares that this phase's coordinator authors or revises
	// the workflow Blueprint. Composition validates the surface capability.
	BlueprintWrite bool
	// Explain is the note the host writes when it enters a phase it holds.
	Explain *PhaseExplain
}

// PhaseExplain tells the person what a host-held phase is doing. Summary is
// the chicklet title; Body says why the step exists and what follows it.
type PhaseExplain struct {
	Summary string
	Body    string
}

// Choice transitions allow explicit human or coordinator actors.
const (
	TransitionActorHuman       = "human"
	TransitionActorCoordinator = "coordinator"
)

// PhaseTransitionDef is a named choice leave from the current phase to To.
type PhaseTransitionDef struct {
	ID     string
	To     string
	Actors []string
	Label  string
	When   string // empty means always armed
}

// PhaseAdvancePolicy selects who may advance workflow phases. The zero value is
// the default: the coordinator calls workflow_advance when gates allow.
type PhaseAdvancePolicy string

// PhaseAdvanceHost: host auto-advances on gate satisfaction; workflow_advance
// stays off the tool surface.
const PhaseAdvanceHost PhaseAdvancePolicy = "host"

func (p PhaseAdvancePolicy) HostOnly() bool {
	return p == PhaseAdvanceHost
}

// PhaseContentReview enables content_apply for tools/paths while in this phase.
type PhaseContentReview struct {
	Tools []string
	Paths []string
}

// CloseoutPolicy selects how a phase treats a bare-prose coordinator finish
// while its completion gates are unmet. The zero value is free: prose may end
// the turn, which is ambient-chat behavior and the default for every phase.
type CloseoutPolicy string

// CloseoutGated holds a no-tool prose finish (bounded per cycle) while the
// completion condition is unmet and no coordinator→human wait is pending.
const CloseoutGated CloseoutPolicy = "gated"

// Gated reports whether the policy is CloseoutGated.
func (p CloseoutPolicy) Gated() bool { return p == CloseoutGated }

// DecisionRejectControls defines host behavior when a user rejects a decision phase.
type DecisionRejectControls struct {
	Pause  bool
	Cancel bool
}

// PauseControls defines host behavior when a workflow run is paused.
type PauseControls struct {
	HoldPending   bool
	CancelRunning bool
}

// StopControls defines host behavior when a workflow run is stopped or exited.
type StopControls struct {
	CancelWorkers   bool
	AbortDelegation bool
	SessionAbort    bool
}

// ReportControls holds controls.report from the workflow YAML block.
type ReportControls struct {
	Enabled bool
	// FindingsLabel names what this workflow calls its assessed conclusions —
	// findings, options, defects. Vocabulary only: sections stay data-decided.
	FindingsLabel string
	// Brief is the rating the report's first page states. Nil when the
	// workflow rates nothing, and the brief states completeness alone.
	Brief *Brief
	// Retries sets the closeout retry limit for report phases when unconfigured per-phase.
	Retries int
}

// ManifestControls holds workflow-level control knobs from YAML.
type ManifestControls struct {
	PhaseAdvance         PhaseAdvancePolicy
	DefaultExecutionMode string
	OnDecisionReject     *DecisionRejectControls
	OnPause              *PauseControls
	OnStop               *StopControls
	ContentReview        *PhaseContentReview
	Report               *ReportControls
}

func (m Manifest) PhaseByID(id string) (PhaseDef, bool) {
	for _, p := range m.PhaseDefs {
		if p.ID == id {
			return p, true
		}
	}
	return PhaseDef{}, false
}

func MergePhaseDef(parent, child PhaseDef) PhaseDef {
	if parent.ID == "" {
		return child
	}
	out := parent
	if child.CompleteWhen != "" {
		out.CompleteWhen = child.CompleteWhen
	}
	if child.EntryWhen != "" {
		out.EntryWhen = child.EntryWhen
	}
	if child.InvokeWorkflow != nil {
		out.InvokeWorkflow = &InvokeWorkflowSpec{
			WorkflowID: child.InvokeWorkflow.WorkflowID,
			Version:    child.InvokeWorkflow.Version,
			Blueprint:  child.InvokeWorkflow.Blueprint,
		}
	}
	if child.InvokeTrigger != "" {
		out.InvokeTrigger = child.InvokeTrigger
	}
	if child.Next != "" {
		out.Next = child.Next
	}
	if child.ChildNext != "" {
		out.ChildNext = child.ChildNext
	}
	if child.ChildCompleteWhen != "" {
		out.ChildCompleteWhen = child.ChildCompleteWhen
		out.ChildGates = append([]string(nil), child.ChildGates...)
	}
	if !child.OnEnter.IsZero() {
		out.OnEnter = mergeOnEnter(out.OnEnter, child.OnEnter)
	}
	if !child.OnReenter.IsZero() {
		out.OnReenter = mergeOnReenter(out.OnReenter, child.OnReenter)
	}
	if len(child.Gates) > 0 {
		out.Gates = append([]string(nil), child.Gates...)
	}
	if child.BindTopologyStage != "" {
		out.BindTopologyStage = child.BindTopologyStage
	}
	if len(child.BindParallelGroup) > 0 {
		out.BindParallelGroup = append([]string(nil), child.BindParallelGroup...)
	}
	if child.ContentReview != nil {
		out.ContentReview = &PhaseContentReview{
			Tools: append([]string(nil), child.ContentReview.Tools...),
			Paths: append([]string(nil), child.ContentReview.Paths...),
		}
	}
	if child.Closeout != "" {
		out.Closeout = child.Closeout
	}
	if child.CloseoutRetries > 0 {
		out.CloseoutRetries = child.CloseoutRetries
	}
	if child.ParallelTask != nil {
		out.ParallelTask = &ParallelTask{
			MaxWorkers:      child.ParallelTask.MaxWorkers,
			MaxReadWorkers:  child.ParallelTask.MaxReadWorkers,
			MaxWriteWorkers: child.ParallelTask.MaxWriteWorkers,
		}
	}
	if child.Fanout.RequireTaskCharter {
		out.Fanout.RequireTaskCharter = true
	}
	if child.Fanout.RequireThreatModel {
		out.Fanout.RequireThreatModel = true
	}
	if child.Fanout.MaxAttempts > 0 {
		out.Fanout.MaxAttempts = child.Fanout.MaxAttempts
	}
	if len(child.TouchPaths) > 0 {
		out.TouchPaths = append([]string(nil), child.TouchPaths...)
	}
	if child.Terminal {
		out.Terminal = true
	}
	if child.CoordinatorSurface != "" {
		out.CoordinatorSurface = child.CoordinatorSurface
	}
	if child.SurfaceTemplate != "" {
		out.SurfaceTemplate = child.SurfaceTemplate
	}
	if len(child.ModeRefs) > 0 {
		out.ModeRefs = append([]string(nil), child.ModeRefs...)
	}
	if child.AdvanceWhenGateMet != "" {
		out.AdvanceWhenGateMet = child.AdvanceWhenGateMet
	}
	if child.LoopExit != "" {
		out.LoopExit = child.LoopExit
	}
	if len(child.Intake) > 0 {
		out.Intake = append([]string(nil), child.Intake...)
	}
	if child.HumanApproval != nil {
		out.HumanApproval = &HumanApprovalConfig{
			Blueprint: child.HumanApproval.Blueprint,
			Readiness: child.HumanApproval.Readiness,
		}
	}
	if child.ReviewLoop != nil {
		out.ReviewLoop = cloneReviewLoop(child.ReviewLoop)
	}
	if child.DepthParam != "" {
		out.DepthParam = child.DepthParam
	}
	if len(child.Transitions) > 0 {
		out.Transitions = copyPhaseTransitions(child.Transitions)
	}
	if child.BlueprintWrite {
		out.BlueprintWrite = true
	}
	if child.Explain != nil {
		explain := *child.Explain
		out.Explain = &explain
	}
	return out
}

// PhaseForRun returns the phase contract effective for a root or invoked run.
func (m Manifest) PhaseForRun(run *api.WorkflowRun, id string) (PhaseDef, bool) {
	def, ok := m.PhaseByID(id)
	if !ok {
		return def, false
	}
	if def.CloseoutRetries == 0 && m.Controls.Report != nil && m.Controls.Report.Retries > 0 && PhaseHasGate(def, "topology_report_delivered") {
		def.CloseoutRetries = m.Controls.Report.Retries
	}
	if !RunHasParent(run) || strings.TrimSpace(def.ChildCompleteWhen) == "" {
		return def, true
	}
	def.CompleteWhen = strings.TrimSpace(def.ChildCompleteWhen)
	def.Gates = append([]string(nil), def.ChildGates...)
	return def, true
}

func copyPhaseTransitions(in []PhaseTransitionDef) []PhaseTransitionDef {
	if len(in) == 0 {
		return nil
	}
	out := make([]PhaseTransitionDef, len(in))
	for i, t := range in {
		out[i] = PhaseTransitionDef{
			ID:     t.ID,
			To:     t.To,
			Actors: append([]string(nil), t.Actors...),
			Label:  t.Label,
			When:   t.When,
		}
	}
	return out
}

// TransitionByID returns the choice edge with id on this phase.
func (p PhaseDef) TransitionByID(id string) (PhaseTransitionDef, bool) {
	id = strings.TrimSpace(id)
	for _, t := range p.Transitions {
		if t.ID == id {
			return t, true
		}
	}
	return PhaseTransitionDef{}, false
}

func copyStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func mergeOnEnter(parent, child PhaseOnEnter) PhaseOnEnter {
	out := parent
	if child.SetPosture != "" {
		out.SetPosture = child.SetPosture
	}
	if child.SetExecutionMode != "" {
		out.SetExecutionMode = child.SetExecutionMode
	}
	if child.RequestUserFeedback != nil {
		out.RequestUserFeedback = child.RequestUserFeedback
	}
	if child.PromptCoordinator {
		out.PromptCoordinator = true
	}
	if len(child.Obligations) > 0 {
		out.Obligations = copyObligationDefs(child.Obligations)
	}
	return out
}

func copyObligationDefs(in []ObligationDef) []ObligationDef {
	if len(in) == 0 {
		return nil
	}
	out := make([]ObligationDef, len(in))
	for i, ob := range in {
		out[i] = ObligationDef{Kind: ob.Kind}
		if len(ob.Params) > 0 {
			params := make(map[string]any, len(ob.Params))
			for k, v := range ob.Params {
				params[k] = v
			}
			out[i].Params = params
		}
	}
	return out
}

func mergeOnReenter(parent, child PhaseOnReenter) PhaseOnReenter {
	out := parent
	if child.InjectKick != "" {
		out.InjectKick = child.InjectKick
	}
	if child.ReenterLeg != "" {
		out.ReenterLeg = child.ReenterLeg
	}
	return out
}

func mergePhaseDefs(parent, child []PhaseDef) []PhaseDef {
	byID := map[string]PhaseDef{}
	order := []string{}
	for _, p := range parent {
		byID[p.ID] = p
		order = append(order, p.ID)
	}
	for _, p := range child {
		if _, ok := byID[p.ID]; !ok {
			order = append(order, p.ID)
		}
		byID[p.ID] = MergePhaseDef(byID[p.ID], p)
	}
	out := make([]PhaseDef, 0, len(order))
	for _, id := range order {
		out = append(out, byID[id])
	}
	return out
}

// rebuildPhaseOrder walks phase next pointers to produce the effective phase sequence.
func rebuildPhaseOrder(defs []PhaseDef) []string {
	if len(defs) == 0 {
		return nil
	}
	byID := make(map[string]PhaseDef, len(defs))
	baseOrder := make([]string, len(defs))
	for i, d := range defs {
		byID[d.ID] = d
		baseOrder[i] = d.ID
	}
	start := baseOrder[0]
	var out []string
	seen := map[string]struct{}{}
	cur := start
	for cur != "" {
		if _, ok := seen[cur]; ok {
			break
		}
		seen[cur] = struct{}{}
		out = append(out, cur)
		def := byID[cur]
		if def.Next != "" {
			cur = def.Next
			continue
		}
		cur = ""
		for i, id := range baseOrder {
			if id == def.ID && i+1 < len(baseOrder) {
				cur = baseOrder[i+1]
				break
			}
		}
	}
	return out
}

// ReachablePhaseIDs returns every phase id reachable from defs[0] via root,
// child, or explicit transition edges. Used for prune keep-set; linear Phases
// may stay next-only.
func ReachablePhaseIDs(defs []PhaseDef) map[string]struct{} {
	keep := map[string]struct{}{}
	if len(defs) == 0 {
		return keep
	}
	byID := make(map[string]PhaseDef, len(defs))
	for _, d := range defs {
		if d.ID == "" {
			continue
		}
		byID[d.ID] = d
	}
	start := defs[0].ID
	if start == "" {
		return keep
	}
	queue := []string{start}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if _, seen := keep[cur]; seen {
			continue
		}
		def, ok := byID[cur]
		if !ok {
			continue
		}
		keep[cur] = struct{}{}
		if n := strings.TrimSpace(def.Next); n != "" {
			if _, exists := byID[n]; exists {
				if _, seen := keep[n]; !seen {
					queue = append(queue, n)
				}
			}
		}
		if n := strings.TrimSpace(def.ChildNext); n != "" {
			if _, exists := byID[n]; exists {
				if _, seen := keep[n]; !seen {
					queue = append(queue, n)
				}
			}
		}
		for _, t := range def.Transitions {
			to := strings.TrimSpace(t.To)
			if to == "" {
				continue
			}
			if _, exists := byID[to]; !exists {
				continue
			}
			if _, seen := keep[to]; !seen {
				queue = append(queue, to)
			}
		}
	}
	return keep
}

// FinalizeManifest rebuilds phase order from phase definitions.
func FinalizeManifest(m Manifest) Manifest {
	if len(m.PhaseDefs) > 0 {
		keep := ReachablePhaseIDs(m.PhaseDefs)
		// Host-closed terminal phases remain reachable.
		for _, d := range m.PhaseDefs {
			if d.Terminal && d.ID != "" {
				keep[d.ID] = struct{}{}
			}
		}
		m.PhaseDefs = prunePhaseDefsToKeepSet(m.PhaseDefs, keep)
		m.Phases = rebuildPhaseOrder(m.PhaseDefs)
	}
	return m
}

// prunePhaseDefsToKeepSet keeps PhaseDefs in declaration order for ids in keep.
func prunePhaseDefsToKeepSet(defs []PhaseDef, keep map[string]struct{}) []PhaseDef {
	if len(defs) == 0 || len(keep) == 0 {
		return defs
	}
	out := make([]PhaseDef, 0, len(keep))
	for _, d := range defs {
		if _, ok := keep[d.ID]; ok {
			out = append(out, d)
		}
	}
	return out
}
