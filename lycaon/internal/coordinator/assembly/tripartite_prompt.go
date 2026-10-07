package assembly

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/capability"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/pkg/api"
)

func (e *AssemblyEngine) renderCoordinatorStablePrompt(
	ctx context.Context,
	pe prompts.PromptTemplateEngine,
	promptRevision string,
	sess *api.Session,
	frame inject.CoordinatorTurnFrame,
	history []api.Message,
	templateRef, posture, settingsFP string,
	turn *TurnAssemblyScratch,
	vars map[string]any,
) (string, error) {
	profile, implState := e.resolveCoordinatorProfile(ctx, sess, frame, history, turn)
	current := surface.ExecutionModeFamily(profile.SurfaceID)
	if turn != nil {
		turn.CurrentExecutionModeFamily = current
	}
	// Every gate the stable block renders keys it.
	if frame.RequiresEvidence("verify") {
		settingsFP += ";verify=1"
	}
	settingsFP += fmt.Sprintf(";verify_command=%s;overlay_promote=%t", strings.TrimSpace(implState.VerifyCommand), len(implState.PendingOverlayIDs) > 0)
	webSearchEnabled := true
	if fn := e.deps().WebSearchEnabled; fn != nil {
		webSearchEnabled = fn()
	}
	settingsFP += stableCapabilityFingerprint(vars, frame.Roster, webSearchEnabled)
	// The standing tool set and omitted units change only at a turn boundary
	// or on request_tools.
	settingsFP += ";loaded=" + loadedFingerprint(e.loadedTools(sess))
	settingsFP += ";omitted=" + loadedFingerprint(e.omittedUnits(sess))
	stableKey := sessionPromptCacheKey(profile, templateRef, sess.AgentType, sess.WorkspacePath, posture, settingsFP, rootCountFromVars(vars), promptRevision)
	if cached, ok := e.cache.LoadStable(sess.ID, stableKey); ok {
		if turn != nil {
			turn.StablePrompt = cached
			turn.StablePromptKey = stableKey
		}
		return cached, nil
	}
	rendered, _, _, err := e.renderTripartiteCoordinatorPrompt(ctx, pe, sess, frame, history, surface.SurfaceSelectionUserPrompt(history), turn, vars)
	if err != nil {
		return "", err
	}
	e.cache.StoreStable(sess.ID, stableKey, rendered)
	if turn != nil {
		turn.StablePrompt = rendered
		turn.StablePromptKey = stableKey
	}
	return rendered, nil
}

func stableCapabilityFingerprint(vars map[string]any, roster *inject.AgentRoster, webSearchEnabled bool) string {
	vision := false
	if caps, ok := vars["caps"].(map[string]any); ok {
		vision, _ = caps["vision"].(bool)
	}
	effectiveAgents := ""
	if roster != nil {
		effectiveAgents = strings.Join(roster.Effective, ",")
	}
	return fmt.Sprintf(";vision=%t;web_search=%t;effective_agents=%s", vision, webSearchEnabled, effectiveAgents)
}

func (e *AssemblyEngine) saveExecutionModeFamily(ctx context.Context, sessionID, family string) {
	if e == nil || e.deps().SaveExecutionModeState == nil {
		return
	}
	e.deps().SaveExecutionModeState(ctx, sessionID, family)
}

func (e *AssemblyEngine) loadExecutionModeState(ctx context.Context, sessionID string) surface.ExecutionModeState {
	if e == nil || e.deps().LoadExecutionModeState == nil {
		return surface.ExecutionModeState{}
	}
	return e.deps().LoadExecutionModeState(ctx, sessionID)
}

func (e *AssemblyEngine) renderTripartiteCoordinatorPrompt(
	ctx context.Context,
	pe prompts.PromptTemplateEngine,
	sess *api.Session,
	frame inject.CoordinatorTurnFrame,
	history []api.Message,
	userPrompt string,
	turn *TurnAssemblyScratch,
	vars map[string]any,
) (string, surface.TurnProfile, surface.TransitionVars, error) {
	if e == nil || pe == nil {
		return "", surface.TurnProfile{}, surface.TransitionVars{}, errPromptEngineNotConfigured
	}
	implState := e.implementSessionState(ctx, sess)
	runCtx := frame.RunContext
	profile := surface.ResolveTurnProfile(runCtx, sess, history, implState)

	surfaceVars := map[string]any{}
	copyPromptVarsSorted(surfaceVars, vars)
	rootCount := 0
	if v, ok := surfaceVars["root_count"].(int); ok {
		rootCount = v
	}
	webSearchEnabled := true
	if fn := e.deps().WebSearchEnabled; fn != nil {
		webSearchEnabled = fn()
	}
	roster := frame.Roster
	if roster == nil {
		// Fixture/dump paths render without an engine-stamped frame.
		declared := runCtx.AllowedAgents
		if len(declared) == 0 {
			declared = spawn.AmbientAllowedAgents()
		}
		resolved := inject.ResolveAgentRoster(profile.SurfaceID, declared, rootCount, false, webSearchEnabled)
		roster = &resolved
	}
	if err := capability.MergeForTurn(surfaceVars, profile, rootCount, roster.Effective, webSearchEnabled); err != nil {
		return "", profile, surface.TransitionVars{}, fmt.Errorf("capability vars: %w", err)
	}
	var toolProfiles []sandbox.ToolProfile
	turnSurface := prompts.SurfaceTurn{Loaded: e.loadedTools(sess)}
	if view := e.sessionCatalogView(ctx, sess); view != nil {
		toolProfiles = view.ToolProfiles
		turnSurface.Schemas = view.ToolSchemas
	}
	if err := prompts.MergeCoordinatorSurfacePathVars(profile.SurfaceID, toolProfiles, surfaceVars, turnSurface); err != nil {
		return "", profile, surface.TransitionVars{}, fmt.Errorf("surface path vars: %w", err)
	}
	if !webSearchEnabled {
		prompts.MergeWebResearchUnavailable(surfaceVars)
	}
	previous := e.loadExecutionModeState(ctx, sess.ID).LastFamily
	current := surface.ExecutionModeFamily(profile.SurfaceID)
	causes := []surface.ModeTransitionCause(nil)
	if turn != nil {
		causes = turn.ModeTransitionCauses
	}
	transition := surface.ComputeModeTransition(previous, current, causes)
	if err := prompts.MergeCoordinatorPromptVars(
		profile.SurfaceID,
		prompts.ExecutionModePromptTransition{
			ExecutionMode:         transition.ExecutionMode,
			ExecutionModePrevious: transition.ExecutionModePrevious,
			ExecutionModeEntered:  transition.ExecutionModeEntered,
			ExecutionModeLeft:     transition.ExecutionModeLeft,
		},
		prompts.CoordinatorPromptGates{
			PendingOverlayPromote: len(implState.PendingOverlayIDs) > 0,
			VerifyRequired:        frame.RequiresEvidence("verify"),
			VerifyCommand:         implState.VerifyCommand,
		},
		surfaceVars,
	); err != nil {
		return "", profile, surface.TransitionVars{}, fmt.Errorf("surface prompt vars: %w", err)
	}
	if err := e.mergeUnitBlocks(ctx, pe, sess, profile.SurfaceID, rootCount, surfaceVars); err != nil {
		return "", profile, transition, fmt.Errorf("unit blocks: %w", err)
	}
	policyVars := spawn.PolicyTemplateVars(e.workerToolBudget(ctx, sess))
	keys := make([]string, 0, len(policyVars))
	for k := range policyVars {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if _, exists := surfaceVars[k]; !exists {
			surfaceVars[k] = policyVars[k]
		}
	}
	if partial := surface.PartialWorkerSummaryJobIDs(history); len(partial) > 0 {
		surfaceVars["partial_worker_jobs"] = partial
	}

	var parts []string
	for _, ref := range []string{surface.CoordinatorCoreTemplate} {
		chunk, err := pe.Render(ctx, ref, surfaceVars)
		if err != nil {
			return "", profile, transition, fmt.Errorf("render %q: %w", ref, err)
		}
		if trimmed := strings.TrimSpace(chunk); trimmed != "" {
			parts = append(parts, trimmed)
		}
	}
	for _, modeRef := range profile.ModeRefs {
		ref := surface.ModeTemplateRef(modeRef)
		chunk, err := pe.Render(ctx, ref, surfaceVars)
		if err != nil {
			return "", profile, transition, fmt.Errorf("render %q: %w", ref, err)
		}
		if trimmed := strings.TrimSpace(chunk); trimmed != "" {
			parts = append(parts, trimmed)
		}
	}
	if profile.SurfaceTemplate != "" {
		chunk, err := pe.Render(ctx, profile.SurfaceTemplate, surfaceVars)
		if err != nil {
			return "", profile, transition, fmt.Errorf("render %q: %w", profile.SurfaceTemplate, err)
		}
		if trimmed := strings.TrimSpace(chunk); trimmed != "" {
			parts = append(parts, trimmed)
		}
	}
	return strings.Join(parts, "\n\n"), profile, transition, nil
}

func copyPromptVarsSorted(dst, src map[string]any) {
	if len(src) == 0 {
		return
	}
	keys := make([]string, 0, len(src))
	for k := range src {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		dst[k] = src[k]
	}
}

func (e *AssemblyEngine) implementSessionState(ctx context.Context, sess *api.Session) surface.ImplementSessionState {
	if e == nil || e.deps().ImplementSessionState == nil || sess == nil {
		return surface.ImplementSessionState{}
	}
	return e.deps().ImplementSessionState(ctx, sess)
}

func sessionPromptCacheKey(profile surface.TurnProfile, templateRef, agentType, projectDir, posture, settingsHash string, rootCount int, promptRevision string) string {
	modeKey := strings.Join(profile.ModeRefs, "+")
	return strings.Join([]string{
		strings.TrimSpace(templateRef),
		strings.TrimSpace(agentType),
		strings.TrimSpace(projectDir),
		strings.TrimSpace(posture),
		profile.SurfaceID,
		modeKey,
		settingsHash,
		"prompts=" + strings.TrimSpace(promptRevision),
		fmt.Sprintf("roots=%d", rootCount),
	}, "|")
}

func rootCountFromVars(vars map[string]any) int {
	if vars == nil {
		return 0
	}
	if v, ok := vars["root_count"].(int); ok {
		return v
	}
	return 0
}

// loadedTools returns the turn ledger's loaded set for the session.
func (e *AssemblyEngine) loadedTools(sess *api.Session) map[string]bool {
	if e == nil || sess == nil || e.deps().LoadedTools == nil {
		return nil
	}
	return e.deps().LoadedTools(sess.ID)
}

func loadedFingerprint(loaded map[string]bool) string {
	names := make([]string, 0, len(loaded))
	for name, on := range loaded {
		if on {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return strings.Join(names, ",")
}
