package loading

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/turnload"
	"github.com/lycaon/lycaon/internal/skills"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

type SkillBodyRenderer func(context.Context, tools.ToolContext, skills.Skill) (string, error)

func (m *Service) SetSkillBodyRenderer(render SkillBodyRenderer) { m.skillBody = render }
func (m *Service) SkillPreload(sessionID string) *turnload.SkillPreload {
	return m.Ledger.Preload(sessionID)
}

// SkillPointer is the skill the session's turn was pointed to without a read.
func (m *Service) SkillPointer(sessionID string) *turnload.SkillRank {
	return m.Ledger.Pointer(sessionID)
}

func (m *Service) selectSkillPreload(ctx context.Context, sess *api.Session, profileID, text string, decision *Decision) {
	if decision.catalog.Turn.Skills.PreloadAt <= 0 || m.skillBody == nil {
		return
	}
	roots, err := m.Workspace.Paths(ctx, sess)
	if err != nil {
		return
	}
	roster, _ := m.skills(ctx, sess, profileID, roots)
	decision.ranking = turnload.RankSkills(ctx, m.Decider(), decision.catalog.Turn.Deadline(), text, roster)
	decision.skill, decision.skillScore = RankedSkillPreload(decision.ranking, roster, decision.catalog.Turn.Skills)
}

func (m *Service) renderSkillPreload(ctx context.Context, sessionID string, tctx tools.ToolContext, decision *Decision) {
	if decision.skill == nil || m.skillBody == nil {
		return
	}
	body, err := m.skillBody(ctx, tctx, *decision.skill)
	if err != nil {
		turnLoadLog.Warn("skill preload render failed", "skill", decision.skill.Name, "error", err)
		return
	}
	if strings.TrimSpace(body) == "" {
		return
	}
	decision.preload = &turnload.SkillPreload{Name: decision.skill.Name, Score: decision.skillScore, Body: body}
	m.Ledger.SetPreload(sessionID, decision.preload)
}

func preloadIdentity(p *turnload.SkillPreload) *turnload.SkillRank {
	if p == nil {
		return nil
	}
	return &turnload.SkillRank{Name: p.Name, Score: p.Score}
}

func RankedSkillPreload(ranking turnload.Ranking, roster []skills.Skill, spec turnload.SkillPreloadSpec) (*skills.Skill, float64) {
	if ranking.Skipped != "" {
		return nil, 0
	}
	top, ok := ranking.Select(spec.PreloadAt, spec.Margin)
	if !ok {
		return nil, 0
	}
	for i := range roster {
		if roster[i].Name == top.Name {
			return &roster[i], top.Score
		}
	}
	return nil, 0
}
