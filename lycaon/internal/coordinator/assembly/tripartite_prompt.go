package assembly

import (
	"context"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/pkg/api"
)

func (e *promptSurface) renderCoordinatorStablePrompt(
	ctx context.Context,
	pe prompts.PromptTemplateEngine,
	promptRevision string,
	sess *api.Session,
	frame inject.CoordinatorTurnFrame,
	history []api.Message,
	turn *TurnAssemblyScratch,
	vars map[string]any,
) (string, error) {
	profile, implState := e.resolveCoordinatorProfile(ctx, sess, frame, history, turn)
	current := surface.ExecutionModeFamily(profile.SurfaceID)
	if turn != nil {
		turn.CurrentExecutionModeFamily = current
	}
	webSearchEnabled := true
	if fn := e.deps.WebSearchEnabled; fn != nil {
		webSearchEnabled = fn()
	}
	rootCount := rootCountFromVars(vars)
	roster := frame.Roster
	if roster == nil {
		declared := frame.RunContext.AllowedAgents
		if len(declared) == 0 {
			declared = spawn.AmbientAllowedAgents()
		}
		resolved := inject.ResolveAgentRoster(profile.SurfaceID, declared, rootCount, false, webSearchEnabled)
		roster = &resolved
	}
	previous := e.loadExecutionModeState(ctx, sess.ID).LastFamily
	causes := []surface.ModeTransitionCause(nil)
	if turn != nil {
		causes = turn.ModeTransitionCauses
	}
	transition := surface.ComputeModeTransition(previous, current, causes)

	in := standingPromptInputs{
		PromptRevision: promptRevision,
		Profile: standingProfileInputs{
			SurfaceID:       profile.SurfaceID,
			SurfaceTemplate: profile.SurfaceTemplate,
			ModeRefs:        profile.ModeRefs,
		},
		Vars:             vars,
		RootCount:        rootCount,
		EffectiveAgents:  roster.Effective,
		WebSearchEnabled: webSearchEnabled,
		LoadedTools:      sortedLoadedSet(e.loadedTools(sess)),
		OmittedUnits:     sortedLoadedSet(e.omittedUnits(sess)),
		ExecutionMode:    transition.ExecutionMode,
		VerifyCommand:    strings.TrimSpace(implState.VerifyCommand),
		WorkerToolBudget: e.workerToolBudget(ctx, sess),
		catalogView:      e.sessionCatalogView(ctx, sess),
	}
	stableKey, err := in.cacheKey()
	if err != nil {
		return "", err
	}
	if cached, ok := e.cache.LoadStable(sess.ID, stableKey); ok {
		if turn != nil {
			turn.StablePrompt = cached
			turn.StablePromptKey = stableKey
		}
		return cached, nil
	}
	rendered, err := renderStandingPrompt(ctx, pe, in)
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

func (e *promptSurface) loadExecutionModeState(ctx context.Context, sessionID string) surface.ExecutionModeState {
	if e == nil || e.deps.LoadExecutionModeState == nil {
		return surface.ExecutionModeState{}
	}
	return e.deps.LoadExecutionModeState(ctx, sessionID)
}

func (e *promptSurface) renderTripartiteCoordinatorPrompt(
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
	profile, implState := e.resolveCoordinatorProfile(ctx, sess, frame, history, turn)
	webSearchEnabled := true
	if fn := e.deps.WebSearchEnabled; fn != nil {
		webSearchEnabled = fn()
	}
	rootCount := rootCountFromVars(vars)
	roster := frame.Roster
	if roster == nil {
		declared := frame.RunContext.AllowedAgents
		if len(declared) == 0 {
			declared = spawn.AmbientAllowedAgents()
		}
		resolved := inject.ResolveAgentRoster(profile.SurfaceID, declared, rootCount, false, webSearchEnabled)
		roster = &resolved
	}
	previous := e.loadExecutionModeState(ctx, sess.ID).LastFamily
	current := surface.ExecutionModeFamily(profile.SurfaceID)
	causes := []surface.ModeTransitionCause(nil)
	if turn != nil {
		causes = turn.ModeTransitionCauses
	}
	transition := surface.ComputeModeTransition(previous, current, causes)

	in := standingPromptInputs{
		Profile: standingProfileInputs{
			SurfaceID:       profile.SurfaceID,
			SurfaceTemplate: profile.SurfaceTemplate,
			ModeRefs:        profile.ModeRefs,
		},
		Vars:             vars,
		RootCount:        rootCount,
		EffectiveAgents:  roster.Effective,
		WebSearchEnabled: webSearchEnabled,
		LoadedTools:      sortedLoadedSet(e.loadedTools(sess)),
		OmittedUnits:     sortedLoadedSet(e.omittedUnits(sess)),
		ExecutionMode:    transition.ExecutionMode,
		VerifyCommand:    strings.TrimSpace(implState.VerifyCommand),
		WorkerToolBudget: e.workerToolBudget(ctx, sess),
		catalogView:      e.sessionCatalogView(ctx, sess),
	}
	rendered, err := renderStandingPrompt(ctx, pe, in)
	if err != nil {
		return "", profile, transition, err
	}
	return rendered, profile, transition, nil
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

func (e *promptSurface) implementSessionState(ctx context.Context, sess *api.Session) surface.ImplementSessionState {
	if e == nil || e.deps.ImplementSessionState == nil || sess == nil {
		return surface.ImplementSessionState{}
	}
	return e.deps.ImplementSessionState(ctx, sess)
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
func (e *promptSurface) loadedTools(sess *api.Session) map[string]bool {
	if e == nil || sess == nil || e.deps.LoadedTools == nil {
		return nil
	}
	return e.deps.LoadedTools(sess.ID)
}

func sortedLoadedSet(loaded map[string]bool) []string {
	if len(loaded) == 0 {
		return nil
	}
	names := make([]string, 0, len(loaded))
	for name, on := range loaded {
		if on {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}
