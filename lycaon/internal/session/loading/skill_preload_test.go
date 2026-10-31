package loading

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/turnload"
	"github.com/lycaon/lycaon/internal/decide/decidetest"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/session/transcript"
	"github.com/lycaon/lycaon/internal/skills"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestSkillPreloadRequiresOneQualifiedCatalogMatch(t *testing.T) {
	roster := []skills.Skill{{Name: "first"}, {Name: "second"}}
	for _, tc := range []struct {
		name                     string
		score, threshold, margin float64
		want                     bool
	}{
		{"below", 3.39, 3.4, 0, false}, {"at", 3.4, 3.4, 0, true}, {"disabled", 4, 0, 0, false}, {"invalid", math.NaN(), 3.4, 0, false}, {"infinite", math.Inf(1), 3.4, 0, false},
		{"clear lead", 3.5, 3.2, 0.3, true}, {"close second", 3.5, 3.2, 1.6, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ranking := turnload.Ranking{Ranks: []turnload.SkillRank{{Name: "second", Score: tc.score}, {Name: "first", Score: 2}}}
			selected, score := RankedSkillPreload(ranking, roster, turnload.SkillPreloadSpec{PreloadAt: tc.threshold, Margin: tc.margin})
			if (selected != nil) != tc.want {
				t.Fatalf("selection=%v", selected)
			}
			if selected != nil && (selected.Name != "second" || score != tc.score) {
				t.Fatalf("selection=%v score=%v", selected, score)
			}
		})
	}
	selected, _ := RankedSkillPreload(turnload.Ranking{Ranks: []turnload.SkillRank{{Name: "unavailable", Score: 4}}}, roster, turnload.SkillPreloadSpec{PreloadAt: 3.4})
	if selected != nil {
		t.Fatal("selected a skill outside the effective catalog")
	}
}

func TestSkillPreloadRecordsOnlySuccessfulRendering(t *testing.T) {
	for _, fail := range []bool{false, true} {
		m := &Service{Ledger: turnload.NewLedger()}
		m.SetSkillBodyRenderer(func(_ context.Context, _ tools.ToolContext, sk skills.Skill) (string, error) {
			if fail {
				return "", errors.New("render failed")
			}
			return "rendered " + sk.Name, nil
		})
		decision := &Decision{skill: &skills.Skill{Name: "first", Resources: []string{"references/guide.md"}}, skillScore: 3.6}
		m.renderSkillPreload(t.Context(), "s", tools.ToolContext{}, decision)
		wire := transcript.TurnLoadWire(decision.receipt())
		if fail {
			if m.SkillPreload("s") != nil || wire.PreloadedSkill != nil {
				t.Fatal("failed rendering was recorded as a preload")
			}
		} else {
			got := m.SkillPreload("s")
			if got == nil || got.Body != "rendered first" || len(got.Resources) != 1 || got.Resources[0] != "references/guide.md" {
				t.Fatalf("preload=%v", got)
			}
			if wire.PreloadedSkill == nil || wire.PreloadedSkill.Name != "first" {
				t.Fatalf("wire preload=%v", wire.PreloadedSkill)
			}
			if strings.Contains(decision.receipt().Decisions, "rendered first") {
				t.Fatal("receipt identity duplicated the procedure body")
			}
		}
	}
}

func TestSelectSkillPreloadGatesStockSkillsByTurnOfferedTools(t *testing.T) {
	roster := []skills.Skill{
		{
			Name:          "review-accessibility",
			Description:   "Audit the rendered web page for accessibility.",
			Body:          "Use `measure_page` and `page_open` to inspect the live page, and optionally `capture_page`.",
			OptionalTools: []string{"capture_page"},
		},
		{
			Name:        "user-skill",
			Description: "Custom project skill.",
			Body:        "Use `measure_page` to inspect the page.",
			UserProvided: true,
		},
	}
	spec := turnload.Catalog{Turn: turnload.TurnSpec{DeadlineMS: 500, Skills: turnload.SkillPreloadSpec{PreloadAt: 3.4, Margin: 0.3}}}
	sess := &api.Session{ID: "s1"}

	// When required tools are absent from floor + active + decision.Tools, the stock skill is filtered out.
	m := &Service{
		Ledger:  turnload.NewLedger(),
		decider: &decidetest.Fake{Scores: []float64{3.8}},
		skills: func(context.Context, *api.Session, string, []string) ([]skills.Skill, []extpacks.Diagnostic) {
			return roster[:1], nil
		},
	}
	m.SetSkillBodyRenderer(func(_ context.Context, _ tools.ToolContext, sk skills.Skill) (string, error) {
		return sk.Body, nil
	})
	decision := &Decision{
		catalog:  spec,
		floor:    []string{"command", "read"},
		decision: turnload.Decision{Tools: map[string]float64{"edit": 0.8, "write": 0.9}},
	}
	m.selectSkillPreload(t.Context(), sess, "coordinator", "make a graphical web page tic tac toe", decision)
	if decision.skill != nil {
		t.Fatalf("stock skill with unoffered required tools was preloaded: %+v", decision.skill)
	}

	// When non-optional tools are offered (via floor, ledger.Active, or decision.Tools), the stock skill is eligible even if optional tools are absent.
	m.Ledger.Activate(sess.ID, []string{"measure_page"}, "measure page geometry")
	decision = &Decision{
		catalog:  spec,
		floor:    []string{"command", "read"},
		decision: turnload.Decision{Tools: map[string]float64{"page_open": 0.9}},
	}
	m.selectSkillPreload(t.Context(), sess, "coordinator", "audit the web page accessibility", decision)
	if decision.skill == nil || decision.skill.Name != "review-accessibility" {
		t.Fatalf("eligible stock skill was not selected: %+v", decision.skill)
	}

	// User-provided skills remain eligible regardless of offered tools.
	m.Ledger = turnload.NewLedger()
	m.skills = func(context.Context, *api.Session, string, []string) ([]skills.Skill, []extpacks.Diagnostic) {
		return roster[1:], nil
	}
	decision = &Decision{
		catalog: spec,
		floor:   []string{"command"},
	}
	m.selectSkillPreload(t.Context(), sess, "coordinator", "custom project workflow", decision)
	if decision.skill == nil || decision.skill.Name != "user-skill" {
		t.Fatalf("user-provided skill was blocked by tool gating: %+v", decision.skill)
	}
}
