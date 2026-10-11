package surface

import (
	"github.com/lycaon/lycaon/internal/toolcontract"

	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/surfacecatalog"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/toolsurface"
	"github.com/lycaon/lycaon/pkg/api"
)

// CoordinatorSurfaceModeRef returns a surface's base mode prompt.
func CoordinatorSurfaceModeRef(surfaceID string) (string, error) {
	catalog, err := surfacecatalog.Load()
	if err != nil {
		return "", err
	}
	row, err := catalog.Surface(surfaceID)
	if err != nil {
		return "", err
	}
	ref := strings.TrimSpace(row.ModeRef)
	return ref, nil
}

// LoadCoordinatorSurfaceActivityLabel returns a surface's activity label.
func LoadCoordinatorSurfaceActivityLabel(surfaceID string) string {
	catalog, err := surfacecatalog.Load()
	if err != nil {
		return ""
	}
	row, err := catalog.Surface(surfaceID)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(row.ActivityLabel)
}

// TurnProfile is the resolved coordinator turn: surface tools and mode partials.
type TurnProfile struct {
	SurfaceID       string
	SurfaceTemplate string
	ModeRefs        []string // len 1 or 2: [base] or [base, overlay]
}

// RunStateOverlay is a mode-agnostic overlay partial suffix (feedback, gates, running).
type RunStateOverlay string

const (
	RunStateSteady   RunStateOverlay = ""
	RunStateFeedback RunStateOverlay = "feedback"
	RunStateGates    RunStateOverlay = "gates"
	RunStateRunning  RunStateOverlay = "running"
)

// ResolveRunStateOverlay selects the highest-priority run-state overlay.
func ResolveRunStateOverlay(runCtx api.CoordinatorRunContext) RunStateOverlay {
	if runCtx.PendingFeedback != nil {
		return RunStateFeedback
	}
	if len(runCtx.FailedLeaves) > 0 {
		return RunStateGates
	}
	if strings.TrimSpace(strings.ToLower(runCtx.RunStatus)) == "running" {
		return RunStateRunning
	}
	return RunStateSteady
}

func investigateProfileBlocksDefault(runCtx api.CoordinatorRunContext) bool {
	if runCtx.WorkflowInvestigateEligible != nil && !*runCtx.WorkflowInvestigateEligible {
		return true
	}
	if s := strings.TrimSpace(runCtx.PhaseCoordinatorSurface); s != "" && s != toolcontract.SurfaceImplementInvestigate {
		return true
	}
	return false
}

func workflowInvestigateEligible(runCtx api.CoordinatorRunContext) bool {
	if runCtx.WorkflowInvestigateEligible != nil {
		return *runCtx.WorkflowInvestigateEligible
	}
	return strings.TrimSpace(runCtx.WorkflowID) == ""
}

func investigateHardBlockActive(
	runCtx api.CoordinatorRunContext,
	history []api.Message,
	state ImplementSessionState,
) bool {
	if runCtx.HasComposeDraft {
		return true
	}
	if investigateProfileBlocksDefault(runCtx) {
		return true
	}
	if len(state.PendingOverlayIDs) > 0 {
		return true
	}
	if childSubroutineBlocksInvestigate(runCtx) {
		return true
	}
	if state.WorkersInFlight > 0 {
		return true
	}
	// Visible user turns regain investigate after structural blocks clear.
	if isVisibleUserTurn(history) {
		return false
	}
	if state.WrapupGatesLoaded && state.OpenRepairSinceUserIntent && HostCycleTurn(history) {
		return false
	}
	if batchPhaseBlocksInvestigateSurface(state.BatchPhase) {
		return true
	}
	if WorkerTaskFinishedTurn(history) && workerTaskFinishedBlocksInvestigate(runCtx, history, state) {
		return true
	}
	if HostLoopWakeTurn(history) && !idleLoopWakeAllowsInvestigate(history) {
		return true
	}
	return false
}

func workerTaskFinishedBlocksInvestigate(
	runCtx api.CoordinatorRunContext,
	history []api.Message,
	state ImplementSessionState,
) bool {
	if mode := normalizeWorkflowDeclaredMode(workflowDeclaredExecutionMode(runCtx)); mode == ExecutionModeFamilyOrchestrate || mode == ExecutionModeFamilyWrapup {
		return true
	}
	if len(state.PendingOverlayIDs) > 0 {
		return true
	}
	if batchPhaseBlocksInvestigateSurface(state.BatchPhase) {
		return true
	}
	capable, ok := prompts.AgentMutationCapable(LatestTerminalWorkerAgentType(history))
	return ok && capable
}

// idleLoopWakeAllowsInvestigate rejects wakes after terminal worker completion.
func idleLoopWakeAllowsInvestigate(history []api.Message) bool {
	since := api.UserIntentBoundary(history)
	if since == 0 {
		return false
	}
	return !TerminalWorkerCompletionSince(history, since)
}

func batchPhaseBlocksInvestigateSurface(phase string) bool {
	switch strings.TrimSpace(strings.ToLower(phase)) {
	case "", "pre_dispatch":
		return false
	default:
		return true
	}
}

func childSubroutineBlocksInvestigate(runCtx api.CoordinatorRunContext) bool {
	return strings.TrimSpace(strings.ToLower(runCtx.RunStatus)) == string(api.WorkflowRunStatusPausedOnChild)
}

// workflowDeclaredExecutionMode prefers the phase stamp over the workflow default.
func workflowDeclaredExecutionMode(runCtx api.CoordinatorRunContext) string {
	if mode := strings.TrimSpace(runCtx.PhaseExecutionMode); mode != "" {
		return mode
	}
	return strings.TrimSpace(runCtx.WorkflowDefaultExecutionMode)
}

func postureSurfaceTemplate(posture api.SessionPosture, surfaceID string) string {
	if surfaceID == toolcontract.SurfaceImplementInvestigate {
		return "agents/coordinator-surface-investigate.md"
	}
	if surfaceID == spawn.SurfaceImplementSynthesis {
		return "agents/coordinator-surface-synthesis.md"
	}
	switch posture {
	case api.SessionPostureOrchestrate:
		return "agents/coordinator-surface-orchestrate.md"
	case api.SessionPostureSpec:
		return "agents/coordinator-surface-spec.md"
	case api.SessionPostureVet:
		return "agents/coordinator-surface-vet.md"
	default:
		return "agents/coordinator-surface-build.md"
	}
}

func resolveSurfaceTemplate(surfaceID string, runCtx api.CoordinatorRunContext, posture api.SessionPosture) string {
	if surfaceID == "workflow_compose" {
		return "agents/coordinator-surface-compose.md"
	}
	if bound, tmpl := manifestBoundSurface(runCtx); bound != "" && bound == surfaceID {
		if tmpl != "" {
			return tmpl
		}
	}
	return postureSurfaceTemplate(posture, surfaceID)
}

func resolveBaseModeRef(surfaceID string, runCtx api.CoordinatorRunContext) (string, error) {
	if refs := runCtx.PhaseModeRefs; len(refs) > 0 {
		if ref := strings.TrimSpace(refs[0]); ref != "" {
			return ref, nil
		}
		return "", fmt.Errorf("phase mode_refs[0] is empty")
	}
	return CoordinatorSurfaceModeRef(surfaceID)
}

// ResolveTurnProfile is the single entry point for coordinator turn resolution.
func ResolveTurnProfile(
	runCtx api.CoordinatorRunContext,
	sess *api.Session,
	history []api.Message,
	state ...ImplementSessionState,
) TurnProfile {
	var sessionState ImplementSessionState
	if len(state) > 0 {
		sessionState = state[0]
	}
	facts := ComputeSurfaceFacts(runCtx, sess, history, sessionState)
	table, err := ShippedCoordinatorFlowTable()
	if err != nil {
		panic(fmt.Sprintf("coordinator flow table: %v", err))
	}
	ev, err := EvaluateFlow(table, facts)
	if err != nil {
		panic(fmt.Sprintf("coordinator flow evaluate: %v", err))
	}
	posture := sessionPosture(sess)
	overlay := ResolveRunStateOverlay(runCtx)
	base, err := resolveBaseModeRef(ev.SurfaceID, runCtx)
	if err != nil {
		panic(fmt.Sprintf("coordinator surface mode: %v", err))
	}
	modeRefs := []string{base}
	if o := overlayModeRef(overlay); o != "" {
		modeRefs = append(modeRefs, o)
	}
	return TurnProfile{
		SurfaceID:       ev.SurfaceID,
		SurfaceTemplate: resolveSurfaceTemplate(ev.SurfaceID, runCtx, posture),
		ModeRefs:        modeRefs,
	}
}

// ResolveTurnProfileForSurface builds a turn profile for one surface.
func ResolveTurnProfileForSurface(
	surfaceID string,
	runCtx api.CoordinatorRunContext,
	sess *api.Session,
) TurnProfile {
	posture := sessionPosture(sess)
	overlay := ResolveRunStateOverlay(runCtx)
	base, err := resolveBaseModeRef(surfaceID, runCtx)
	if err != nil {
		panic(fmt.Sprintf("coordinator surface mode: %v", err))
	}
	modeRefs := []string{base}
	if o := overlayModeRef(overlay); o != "" {
		modeRefs = append(modeRefs, o)
	}
	return TurnProfile{
		SurfaceID:       surfaceID,
		SurfaceTemplate: resolveSurfaceTemplate(surfaceID, runCtx, posture),
		ModeRefs:        modeRefs,
	}
}

func sessionPosture(sess *api.Session) api.SessionPosture {
	if sess == nil || sess.Posture == "" {
		return api.SessionPostureBuild
	}
	return sess.Posture
}

func manifestBoundSurface(runCtx api.CoordinatorRunContext) (string, string) {
	surfaceID := strings.TrimSpace(runCtx.PhaseCoordinatorSurface)
	if surfaceID == "" {
		return "", ""
	}
	return surfaceID, strings.TrimSpace(runCtx.PhaseSurfaceTemplate)
}

func overlayModeRef(overlay RunStateOverlay) string {
	if overlay == RunStateSteady {
		return ""
	}
	return string(overlay)
}

// CompileToolPlan resolves a surface's floor as immediate and its loadable
// tools as deferred. The turn's loaded set is promoted by the prompt loop.
func CompileToolPlan(profile TurnProfile, rootCount int) (toolsurface.Plan, error) {
	catalog, err := surfacecatalog.Load()
	if err != nil {
		return toolsurface.Plan{}, err
	}
	row, err := catalog.Surface(profile.SurfaceID)
	if err != nil {
		return toolsurface.Plan{}, err
	}
	return compileToolPlan(row, rootCount)
}

// CompileToolPlans resolves every declared coordinator surface.
func CompileToolPlans(rootCount int) (map[string]toolsurface.Plan, error) {
	catalog, err := surfacecatalog.Load()
	if err != nil {
		return nil, err
	}
	out := make(map[string]toolsurface.Plan, len(catalog.SurfaceIDs()))
	for _, id := range catalog.SurfaceIDs() {
		row, rowErr := catalog.Surface(id)
		if rowErr != nil {
			return nil, rowErr
		}
		plan, planErr := compileToolPlan(row, rootCount)
		if planErr != nil {
			return nil, planErr
		}
		out[id] = plan
	}
	return out, nil
}

func compileToolPlan(row surfacecatalog.Surface, rootCount int) (toolsurface.Plan, error) {
	immediate, err := FilterSurfaceToolsForRootCount(row.FloorTools(), rootCount)
	if err != nil {
		return toolsurface.Plan{}, err
	}
	deferred, err := FilterSurfaceToolsForRootCount(row.LoadableTools(), rootCount)
	if err != nil {
		return toolsurface.Plan{}, err
	}
	return toolsurface.Compile(immediate, deferred), nil
}

// ModeTemplateRef returns the pongo ref for a mode partial suffix.
func ModeTemplateRef(modeRef string) string {
	return "agents/coordinator-mode-" + strings.TrimSpace(modeRef) + ".md"
}
