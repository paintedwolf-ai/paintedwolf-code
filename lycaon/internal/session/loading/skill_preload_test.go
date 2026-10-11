package loading

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/turnload"
	"github.com/lycaon/lycaon/internal/session/transcript"
	"github.com/lycaon/lycaon/internal/skills"
	"github.com/lycaon/lycaon/internal/tools"
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
		decision := &Decision{skill: &skills.Skill{Name: "first"}, skillScore: 3.6}
		m.renderSkillPreload(t.Context(), "s", tools.ToolContext{}, decision)
		wire := transcript.TurnLoadWire(decision.receipt())
		if fail {
			if m.SkillPreload("s") != nil || wire.PreloadedSkill != nil {
				t.Fatal("failed rendering was recorded as a preload")
			}
		} else {
			if got := m.SkillPreload("s"); got == nil || got.Body != "rendered first" {
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
