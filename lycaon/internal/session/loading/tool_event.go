package loading

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/coordinator/guard"
	"github.com/lycaon/lycaon/internal/coordinator/turnload"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/session/transcript"
	"github.com/lycaon/lycaon/internal/skills"
	"github.com/lycaon/lycaon/internal/toolpolicy"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/toolsurface"
	"github.com/lycaon/lycaon/pkg/api"
)

// ObserveToolCall records a tool call and ranks tool-event skills on the
// turn's first non-skipped loadable tool.
func (m *Service) ObserveToolCall(ctx context.Context, sess *api.Session, tctx tools.ToolContext, tool string) {
	if m == nil || sess == nil {
		return
	}
	m.Ledger.Use(sess.ID, tool)
	m.readSkillForTool(ctx, sess, tctx, tool)
}

func (m *Service) readSkillForTool(ctx context.Context, sess *api.Session, tctx tools.ToolContext, tool string) {
	if m.skillBody == nil || m.skills == nil || m.Ledger.FullCatalog(sess.ID) {
		return
	}
	catalog, err := turnload.LoadCatalog()
	if err != nil || (catalog.ToolEvent.ReadAt <= 0 && catalog.ToolEvent.PointerAt <= 0) || catalog.ToolEvent.Skips(tool) {
		return
	}
	request, ok := m.Ledger.ClaimToolEvent(sess.ID, tool)
	if !ok {
		return
	}
	started := time.Now()
	roots, err := m.Workspace.Paths(ctx, sess)
	if err != nil {
		return
	}
	roster, _ := m.skills(ctx, sess, tctx.Identity.Agent, roots)
	if reader, rErr := m.toolEventReader(ctx, sess, tctx, tool, len(roots)); rErr == nil {
		roster = eligibleSkillsForReader(roster, reader)
	} else {
		roster = eligibleSkillsForOfferedTools(roster, m.toolEventOfferedTools(ctx, sess, tctx, tool, len(roots)))
	}
	card := turnload.ToolCard{Name: tool, Description: m.toolDescription(ctx, sess, tctx.Identity.Agent, tctx.Turn.TurnSurfaceID, tool)}
	finishDeciding := m.beginDeciding(ctx, sess, sess.ID, api.TurnLoadTriggerToolEvent, tctx.Identity.ToolCallID)
	outcome := turnload.RankToolEvent(ctx, m.Decider(), catalog.ToolEvent, request, card, roster)
	finishDeciding()
	m.Ledger.ExposeCatalog(sess.ID, outcome.Ranking.Failure)
	var preload *turnload.SkillPreload
	switch {
	case outcome.Read != nil:
		preload = m.renderToolEventSkill(ctx, sess.ID, tctx, tool, *outcome.Read, roster)
	case outcome.Pointer != nil:
		m.Ledger.SetPointer(sess.ID, outcome.Pointer)
	}
	stateSurface := strings.TrimSpace(tctx.Turn.TurnSurfaceID)
	if stateSurface == "" {
		stateSurface = strings.TrimSpace(tctx.Identity.Agent)
	}
	m.recordTurnLoad(ctx, sess, sess.ID, store.TurnLoadReceipt{
		Trigger:    store.TurnLoadTriggerToolEvent,
		ToolCallID: tctx.Identity.ToolCallID,
		SurfaceID:  stateSurface,
		Engine:     transcript.EngineLabel(outcome.Engine),
		StateJSON:  marshalJSON(map[string]any{"need": turnload.BoundUser(outcome.Need, catalog.State.UserTextChars), "surface": stateSurface, "tool": tool}),
		Decisions:  marshalJSON(map[string]any{"tool_event": outcome, "preloaded_skill": preloadIdentity(preload)}),
		ElapsedMs:  time.Since(started).Milliseconds(),
		Abstained:  outcome.Abstained,
		Reason:     outcome.Reason,
	})
	turnLoadLog.Info("tool event decided",
		"session_id", sess.ID,
		"tool", tool,
		"read", outcome.Read != nil,
		"pointer", outcome.Pointer != nil,
		"skills", outcome.Names(),
		"abstained", outcome.Abstained,
		"elapsed_ms", time.Since(started).Milliseconds())
}

func (m *Service) toolEventReader(ctx context.Context, sess *api.Session, tctx tools.ToolContext, tool string, rootCount int) (prompts.Reader, error) {
	var profiles []sandbox.ToolProfile
	if m != nil && m.profiles != nil {
		profiles = m.profiles(ctx, sess)
	}
	if len(profiles) == 0 {
		var err error
		profiles, err = sandbox.LoadToolProfiles()
		if err != nil {
			return prompts.Reader{}, err
		}
	}
	profileID := strings.TrimSpace(tctx.Identity.Agent)
	if mapped, err := prompts.ToolProfileForAgent(profileID); err == nil && mapped != "" {
		profileID = mapped
	}
	if profileID == "" {
		profileID = prompts.CoordinatorProfileID
	}
	surfaceID := ""
	if guard.IsCoordinatorProfile(profileID) {
		if cand := strings.TrimSpace(tctx.Turn.TurnSurfaceID); prompts.IsCoordinatorSurfaceID(cand) {
			surfaceID = cand
		}
	}
	sessionMode := string(api.SessionModeAgent)
	if tctx.Source.AskMode || (sess != nil && sess.Mode == api.SessionModeAsk) {
		sessionMode = string(api.SessionModeAsk)
	}
	offered := m.toolEventOfferedTools(ctx, sess, tctx, tool, rootCount)
	visible := make([]string, 0, len(offered))
	for name := range offered {
		visible = append(visible, name)
	}
	sort.Strings(visible)
	return prompts.NewReader(prompts.ReaderInput{
		ProfileID:        profileID,
		SurfaceID:        surfaceID,
		SessionMode:      sessionMode,
		Profiles:         profiles,
		Plan:             toolsurface.Compile(visible, nil),
		Loaded:           offered,
		Visible:          visible,
		ProjectedSchemas: tctx.Turn.TurnOfferedToolSchemas,
		RootCount:        rootCount,
		WebSearch:        true,
	})
}

func (m *Service) toolEventOfferedTools(ctx context.Context, sess *api.Session, tctx tools.ToolContext, tool string, rootCount int) map[string]bool {
	floor, loadable, _ := m.turnToolSets(ctx, sess, tctx.Identity.Agent, tctx.Turn.TurnSurfaceID, rootCount)
	offered := nameSet(floor)
	for _, name := range loadable {
		if name = strings.TrimSpace(name); name != "" {
			offered[name] = true
		}
	}
	for _, name := range tctx.Turn.TurnOfferedToolNames {
		if name = strings.TrimSpace(name); name != "" {
			offered[name] = true
		}
	}
	for _, name := range tctx.Turn.TurnToolPlan.ImmediateNames() {
		if name = strings.TrimSpace(name); name != "" {
			offered[name] = true
		}
	}
	for _, name := range tctx.Turn.TurnToolPlan.DeferredNames() {
		if name = strings.TrimSpace(name); name != "" {
			offered[name] = true
		}
	}
	if m != nil && m.Ledger != nil && sess != nil {
		for name := range m.Ledger.Active(sess.ID) {
			offered[name] = true
		}
	}
	if tool = strings.TrimSpace(tool); tool != "" {
		offered[tool] = true
	}
	return offered
}

// renderToolEventSkill renders the selected skill's body into the turn ledger.
func (m *Service) renderToolEventSkill(ctx context.Context, sessionID string, tctx tools.ToolContext, tool string, read turnload.SkillRank, roster []skills.Skill) *turnload.SkillPreload {
	for i := range roster {
		if roster[i].Name != read.Name {
			continue
		}
		body, err := m.skillBody(ctx, tctx, roster[i])
		if err != nil {
			turnLoadLog.Warn("tool event skill render failed", "skill", read.Name, "tool", tool, "error", err)
			return nil
		}
		if strings.TrimSpace(body) == "" {
			return nil
		}
		preload := &turnload.SkillPreload{
			Name:      read.Name,
			Score:     read.Score,
			Body:      body,
			Tool:      tool,
			Resources: append([]string(nil), roster[i].Resources...),
		}
		m.Ledger.SetPreload(sessionID, preload)
		return preload
	}
	return nil
}

// toolDescription returns the policy description for tool on profileID.
func (m *Service) toolDescription(ctx context.Context, sess *api.Session, profileID, surfaceID, tool string) string {
	if m == nil || m.policy == nil {
		return ""
	}
	policy := m.policy()
	if policy == nil {
		return ""
	}
	listCtx := ctx
	if guard.IsCoordinatorProfile(profileID) && surfaceID != "" {
		listCtx = toolpolicy.WithCoordinatorSurface(ctx, surfaceID)
	}
	for _, meta := range policy.ListForPrompt(listCtx, sess, profileID) {
		if meta.Name == tool {
			return meta.Description
		}
	}
	return ""
}
