package promptloop

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/guard"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/toolscope"
	"github.com/lycaon/lycaon/internal/toolsurface"
	"github.com/lycaon/lycaon/internal/webresearch"
	"github.com/lycaon/lycaon/pkg/api"
)

// compileWorkerToolPlan starts from profile-filtered capabilities, never the coordinator roster.
func (l *promptContext) compileWorkerToolPlan(ctx context.Context, sess *api.Session, metas []tools.ToolMeta, activated map[string]bool) toolsurface.Plan {
	mcp := l.mcpToolPlan(ctx, sess, metas, activated)
	var immediate, deferred []string
	for _, meta := range metas {
		if !l.webSearchEnabled() && (meta.Name == webresearch.SearchToolName || meta.Name == webresearch.FetchURLToolName) {
			continue
		}
		eager := !meta.Deferred
		if meta.IsMCP() {
			eager = mcp.Eager(meta.Name)
		}
		if eager {
			immediate = append(immediate, meta.Name)
		} else {
			deferred = append(deferred, meta.Name)
		}
	}
	plan := toolsurface.Compile(immediate, deferred)
	for name := range activated {
		if plan.Addressable(name) {
			plan = plan.Promote(name)
		}
	}
	for _, name := range toolcontract.Implied(l.liveResources(sess)) {
		if plan.Addressable(name) {
			plan = plan.Promote(name)
		}
	}
	return plan
}

func (l *promptContext) webSearchEnabled() bool {
	if l == nil || l.Deps.WebSearchEnabled == nil {
		return true
	}
	return l.Deps.WebSearchEnabled()
}

func surfaceAllowsTool(plan toolsurface.Plan, surfaceID, toolName string) bool {
	surfaceID = strings.TrimSpace(surfaceID)
	// Workers and unconfigured hosts have no coordinator surface compile.
	if surfaceID == "" && !plan.Compiled() {
		return true
	}
	return plan.Compiled() && plan.Addressable(toolName)
}

func offSurfaceCode(toolName string) string {
	if toolcontract.MutatesContent(toolName) {
		return "COORDINATOR_ORCHESTRATE_WRITE_DENIED"
	}
	return "COORDINATOR_TOOL_DENIED"
}

func (l *promptContext) coordinatorToolsForTurn(
	ctx context.Context,
	sess *api.Session,
	profileID string,
	history []api.Message,
	userPrompt string,
	iterIndex, maxIter int,
	frame inject.CoordinatorTurnFrame,
	st *promptLoopTurnState,
) ([]tools.ToolMeta, toolsurface.Plan, string, error) {
	if l == nil {
		return nil, toolsurface.Plan{}, "", nil
	}
	if st != nil && st.proseTurn(sess, iterIndex, maxIter) {
		st.proseFinish = true
	}
	surfaceID := ""
	if guard.IsCoordinatorProfile(profileID) {
		surfaceID = l.resolveTurnProfile(ctx, sess, history, frame).SurfaceID
	}
	if l.Deps.Policy == nil {
		return nil, toolsurface.Plan{}, surfaceID, nil
	}
	all := l.Deps.Policy.ListForPrompt(ctx, sess, profileID)
	if frame.Machine.Compiled() {
		all = tools.HideSkillsReadWhenEmpty(all, frame.Machine.SkillCount)
	}
	activated := l.activeDeferredTools(sess)
	if profileID == prompts.CoordinatorProfileID {
		all = l.appendActivatedOpenWorldMetas(all, activated)
	}
	if profileID != prompts.CoordinatorProfileID {
		plan := l.compileWorkerToolPlan(ctx, sess, all, activated)
		filtered := make([]tools.ToolMeta, 0, len(all))
		for _, meta := range all {
			if plan.Immediate(meta.Name) {
				filtered = append(filtered, meta)
			}
		}
		if st != nil && st.proseTurn(sess, iterIndex, maxIter) {
			filtered = workerProseToolMetas(filtered, sess)
			var names []string
			for _, meta := range filtered {
				names = append(names, meta.Name)
			}
			plan = toolsurface.Compile(names, nil)
		}
		return trimPromptToolMetas(filtered), plan, "", nil
	}
	profile := surface.TurnProfile{SurfaceID: surfaceID}
	rootCount := l.projectRootCount(ctx, sess)
	plan, err := surface.CompileToolPlan(profile, rootCount)
	if err != nil {
		return nil, toolsurface.Plan{}, surfaceID, fmt.Errorf("compile coordinator tool surface %q: %w", surfaceID, err)
	}
	taskAllowlist := taskSpawnAllowlistForTurn(frame)
	openWorld := plan.Immediate("request_tools")
	if openWorld {
		all = appendMissingMetas(all, l.liveMCPMetas())
	}
	mcpPlan := l.mcpToolPlan(ctx, sess, all, activated)
	plan = l.compileRuntimeToolPlan(plan, frame.RunContext, all, activated, mcpPlan, openWorld, sess)
	plan = filterToolPlanForRootCount(plan, rootCount)
	return trimCoordinatorToolMetasForPlan(plan, all, taskAllowlist, phaseVerdictSchema(frame)), plan, surfaceID, nil
}

func filterToolPlanForRootCount(plan toolsurface.Plan, rootCount int) toolsurface.Plan {
	if rootCount != 0 {
		return plan
	}
	for _, name := range plan.AddressableNames() {
		if toolscope.RequiresProjectRoots(name) {
			plan = plan.Without(name)
		}
	}
	return plan
}

// phaseVerdictSchema is the submit_verdict call the active review phase
// accepts, or nil outside one.
func phaseVerdictSchema(frame inject.CoordinatorTurnFrame) map[string]any {
	if exit := frame.Runtime.PhaseExit; exit != nil {
		return exit.SubmitVerdictArgsSchema
	}
	return nil
}

// trimCoordinatorToolMetasForPlan narrows the offered tools to the plan. A
// review phase's submit_verdict carries that phase's call schema, which is
// also the schema admission checks.
func trimCoordinatorToolMetasForPlan(plan toolsurface.Plan, all []tools.ToolMeta, taskAllowlist []string, verdictSchema map[string]any) []tools.ToolMeta {
	out := make([]tools.ToolMeta, 0, len(all))
	for _, meta := range all {
		if !plan.Immediate(meta.Name) {
			continue
		}
		if meta.Name == "submit_verdict" && verdictSchema != nil {
			meta.ArgsSchema = verdictSchema
		}
		outMeta := tools.TrimCoordinatorToolMeta(meta)
		if meta.Name == "task" {
			if taskAllowlist != nil && len(taskAllowlist) == 0 {
				continue
			}
			outMeta = guard.ApplyTaskSpawnAllowlistSchema(outMeta, taskAllowlist)
		}
		out = append(out, outMeta)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (l *promptContext) compileRuntimeToolPlan(
	plan toolsurface.Plan,
	runCtx api.CoordinatorRunContext,
	metas []tools.ToolMeta,
	activated map[string]bool,
	mcpPlan tools.MCPToolPlan,
	openWorld bool,
	sess *api.Session,
) toolsurface.Plan {
	if strings.TrimSpace(runCtx.AdvanceWhenGateMet) == "coordinator" {
		plan = plan.Promote("workflow_advance")
	}
	if !l.webSearchEnabled() {
		plan = plan.Without(webresearch.SearchToolName, webresearch.FetchURLToolName)
	}
	plan = plan.Promote(toolcontract.Implied(l.liveResources(sess))...)
	for name := range activated {
		if plan.Addressable(name) || !toolcontract.IsCatalog(name) {
			plan = plan.Promote(name)
		}
	}
	if !openWorld {
		return plan
	}
	for _, meta := range metas {
		if !meta.IsMCP() || plan.Addressable(meta.Name) {
			continue
		}
		if mcpPlan.Eager(meta.Name) {
			plan = plan.Promote(meta.Name)
		} else {
			plan = plan.Defer(meta.Name)
		}
	}
	return plan
}

func (l *promptContext) mcpToolPlan(ctx context.Context, sess *api.Session, metas []tools.ToolMeta, activated map[string]bool) tools.MCPToolPlan {
	if l != nil && l.Deps.MCPAlwaysLoad != nil {
		modes := l.Deps.MCPAlwaysLoad(ctx, sess)
		resolved := make([]tools.ToolMeta, len(metas))
		copy(resolved, metas)
		for i := range resolved {
			if resolved[i].IsMCP() && strings.TrimSpace(resolved[i].SourceID) != "" {
				if always, ok := modes[resolved[i].SourceID]; ok {
					resolved[i].AlwaysLoad = always
				}
			}
		}
		metas = resolved
	}
	return tools.PlanMCPTools(metas, activated)
}

// appendActivatedOpenWorldMetas adds activated non-catalog tools that ListForPrompt omitted.
func (l *promptContext) appendActivatedOpenWorldMetas(all []tools.ToolMeta, activated map[string]bool) []tools.ToolMeta {
	if l == nil || l.Deps.Tools == nil || len(activated) == 0 {
		return all
	}
	have := make(map[string]bool, len(all))
	for _, meta := range all {
		have[meta.Name] = true
	}
	extras := make([]tools.ToolMeta, 0)
	for name := range activated {
		if have[name] || toolcontract.IsCatalog(name) {
			continue
		}
		def, ok := l.Deps.Tools.Definition(name)
		if !ok {
			continue
		}
		extras = append(extras, def.Meta)
	}
	return appendMissingMetas(all, extras)
}

func appendMissingMetas(all, extras []tools.ToolMeta) []tools.ToolMeta {
	if len(extras) == 0 {
		return all
	}
	have := make(map[string]bool, len(all))
	for _, meta := range all {
		have[meta.Name] = true
	}
	add := make([]tools.ToolMeta, 0)
	for _, meta := range extras {
		if have[meta.Name] || strings.TrimSpace(meta.Name) == "" {
			continue
		}
		have[meta.Name] = true
		add = append(add, meta)
	}
	if len(add) == 0 {
		return all
	}
	sort.Slice(add, func(i, j int) bool { return add[i].Name < add[j].Name })
	return append(all, add...)
}

// trimPromptToolMetas applies the provider-safe schema projection.
func trimPromptToolMetas(metas []tools.ToolMeta) []tools.ToolMeta {
	out := make([]tools.ToolMeta, len(metas))
	for i, meta := range metas {
		out[i] = tools.TrimCoordinatorToolMeta(meta)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (l *promptContext) liveResources(sess *api.Session) toolcontract.ResourcePresence {
	if l == nil || l.Deps.LiveResources == nil || sess == nil {
		return toolcontract.ResourcePresence{}
	}
	return l.Deps.LiveResources(sess.ID)
}

func (l *promptContext) liveMCPMetas() []tools.ToolMeta {
	if l == nil || l.Deps.Tools == nil {
		return nil
	}
	out := make([]tools.ToolMeta, 0)
	for _, meta := range l.Deps.Tools.List() {
		if meta.IsMCP() {
			out = append(out, meta)
		}
	}
	return out
}

// taskSpawnAllowlistForTurn returns nil for no roster and empty for no agents.
func taskSpawnAllowlistForTurn(frame inject.CoordinatorTurnFrame) []string {
	if frame.Roster == nil {
		return nil
	}
	if frame.Roster.Effective == nil {
		return []string{}
	}
	return frame.Roster.Effective
}

func (l *promptContext) projectRootCount(ctx context.Context, sess *api.Session) int {
	if l != nil && l.Deps.ProjectRootCount != nil && sess != nil {
		return l.Deps.ProjectRootCount(ctx, sess)
	}
	if sess != nil && strings.TrimSpace(sess.WorkspacePath) != "" {
		return 1
	}
	return 0
}

func (l *promptContext) activeDeferredTools(sess *api.Session) map[string]bool {
	if l == nil || l.Deps.LoadedTools == nil || sess == nil {
		return nil
	}
	return l.Deps.LoadedTools(sess.ID)
}

func (l *promptContext) resolveTurnProfile(
	ctx context.Context,
	sess *api.Session,
	history []api.Message,
	frame inject.CoordinatorTurnFrame,
) surface.TurnProfile {
	implState := l.implementSessionState(ctx, sess)
	return surface.ResolveTurnProfile(frame.RunContext, sess, history, implState)
}

func (l *promptContext) implementSessionState(ctx context.Context, sess *api.Session) surface.ImplementSessionState {
	if l == nil || l.Deps.ImplementSessionState == nil || sess == nil {
		return surface.ImplementSessionState{}
	}
	return l.Deps.ImplementSessionState(ctx, sess)
}
