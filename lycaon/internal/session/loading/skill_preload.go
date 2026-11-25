package loading

import (
	"context"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/guard"
	"github.com/lycaon/lycaon/internal/coordinator/turnload"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/promptunit"
	"github.com/lycaon/lycaon/internal/skills"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/toolsurface"
	"github.com/lycaon/lycaon/internal/toolvocab"
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
	if decision.decision.Failure.RequiresFullCatalog() || decision.catalog.Turn.Skills.PreloadAt <= 0 || m.skillBody == nil || m.skills == nil {
		return
	}
	roots, err := m.Workspace.Paths(ctx, sess)
	if err != nil {
		return
	}
	reader, err := m.decisionReader(ctx, sess, profileID, decision)
	if err != nil {
		return
	}
	roster, _ := m.skills(ctx, sess, profileID, roots)
	roster = eligibleSkillsForReader(roster, reader)
	decision.ranking = turnload.RankSkills(ctx, m.Decider(), decision.catalog.Turn.Deadline(), text, roster)
	decision.skill, decision.skillScore = RankedSkillPreload(decision.ranking, roster, decision.catalog.Turn.Skills)
}

// decisionReader projects the request this decision's loads would carry: its
// floor and every loaded tool offered.
func (m *Service) decisionReader(ctx context.Context, sess *api.Session, profileID string, decision *Decision) (prompts.Reader, error) {
	if m == nil || m.hostFacts == nil {
		return prompts.Reader{}, fmt.Errorf("turn load: session host facts not configured")
	}
	host, err := m.hostFacts(ctx, sess)
	if err != nil {
		return prompts.Reader{}, err
	}
	if profileID == "" {
		profileID = prompts.CoordinatorProfileID
	}
	loaded := nameSet(decision.loadable)
	if m.Ledger != nil && sess != nil {
		for name := range m.Ledger.Active(sess.ID) {
			loaded[name] = true
		}
	}
	for name := range decision.decision.Tools {
		loaded[name] = true
	}
	loadable := sortedToolSet(loaded)
	surfaceID := ""
	if decision.host == promptunit.HostCoordinator || guard.IsCoordinatorProfile(profileID) {
		if cand := strings.TrimSpace(decision.state.Surface); prompts.IsCoordinatorSurfaceID(cand) {
			surfaceID = cand
		}
	}
	plan := toolsurface.Compile(decision.floor, loadable).Promote(loadable...)
	return prompts.NewReader(host, prompts.TurnProjection{
		ProfileID: profileID,
		SurfaceID: surfaceID,
		Plan:      plan,
		Offered:   plan.ImmediateNames(),
	})
}

func eligibleSkillsForReader(roster []skills.Skill, reader prompts.Reader) []skills.Skill {
	out := make([]skills.Skill, 0, len(roster))
	for _, sk := range roster {
		if toolvocab.SkillToolsOfferedForReader(sk, reader) {
			out = append(out, sk)
		}
	}
	return out
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
	decision.preload = &turnload.SkillPreload{
		Name:      decision.skill.Name,
		Score:     decision.skillScore,
		Body:      body,
		Resources: append([]string(nil), decision.skill.Resources...),
	}
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
