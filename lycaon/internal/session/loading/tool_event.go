package loading

import (
	"context"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/coordinator/turnload"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/session/transcript"
	"github.com/lycaon/lycaon/internal/skills"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

// ObserveToolCall records a tool the chat called and, on the turn's first
// loadable tool, asks the engine which skill that tool's work needs.
func (m *Service) ObserveToolCall(ctx context.Context, sess *api.Session, tctx tools.ToolContext, tool string) {
	if m == nil || sess == nil {
		return
	}
	m.Ledger.Use(sess.ID, tool)
	m.readSkillForTool(ctx, sess, tctx, tool)
}

func (m *Service) readSkillForTool(ctx context.Context, sess *api.Session, tctx tools.ToolContext, tool string) {
	if m.skillBody == nil {
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
	card := turnload.ToolCard{Name: tool, Description: m.toolDescription(ctx, sess, tctx.Identity.Agent, tool)}
	finishDeciding := m.beginDeciding(ctx, sess, sess.ID, api.TurnLoadTriggerToolEvent, tctx.Identity.ToolCallID)
	outcome := turnload.RankToolEvent(ctx, m.Decider(), catalog.ToolEvent, request, card, roster)
	finishDeciding()
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

// renderToolEventSkill reads the selected skill's body into the turn; a body
// that fails to render leaves the turn without a skill.
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
		preload := &turnload.SkillPreload{Name: read.Name, Score: read.Score, Body: body, Tool: tool}
		m.Ledger.SetPreload(sessionID, preload)
		return preload
	}
	return nil
}

// toolDescription is the bounded description the engine reads for a tool,
// from the same policy that offers the tool to the prompt.
func (m *Service) toolDescription(ctx context.Context, sess *api.Session, profileID, tool string) string {
	policy := m.policy()
	if policy == nil {
		return ""
	}
	for _, meta := range policy.ListForPrompt(ctx, sess, profileID) {
		if meta.Name == tool {
			return meta.Description
		}
	}
	return ""
}
