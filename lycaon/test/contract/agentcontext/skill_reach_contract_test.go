package contract

import (
	"testing"

	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/skills"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/laws/promptrender"
)

// forEachStockSkillRender calls check for every stock skill rendered for
// every reader that can select it.
func forEachStockSkillRender(t *testing.T, check func(t *testing.T, sk skills.Skill, readerLabel string, reader prompts.Reader, vars map[string]any, parts []promptrender.Text)) {
	t.Helper()
	stock, err := promptrender.LoadStock(t.Context())
	contractcheck.FailErr(t, "load the stock catalog", err)
	renders, err := promptrender.RenderStockSkills(stock)
	contractcheck.FailErr(t, "render the stock skills", err)
	for _, sr := range renders {
		t.Run(sr.Reader+"/"+sr.Skill.Name, func(t *testing.T) {
			vars, err := prompts.SkillVars(sr.State)
			contractcheck.FailErr(t, "skill vars", err)
			check(t, sr.Skill, sr.Reader, sr.State, vars, sr.Parts)
		})
	}
}

// stockSkillCorpus loads the stock pack skills with their bundled resources.
func stockSkillCorpus(t *testing.T) []skills.Skill {
	t.Helper()
	stock, err := promptrender.LoadStock(t.Context())
	contractcheck.FailErr(t, "load the stock catalog", err)
	loaded, err := promptrender.StockSkills(stock)
	contractcheck.FailErr(t, "load the stock skills", err)
	return loaded
}

// renderSkillParts renders a skill and its markdown resources as a read
// delivers them.
func renderSkillParts(t *testing.T, sk skills.Skill, vars map[string]any) []promptrender.Text {
	t.Helper()
	parts, err := promptrender.RenderSkill(sk, vars)
	contractcheck.FailErr(t, "render "+sk.Name, err)
	return parts
}
