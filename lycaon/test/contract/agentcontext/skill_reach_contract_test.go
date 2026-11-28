package contract

import (
	"testing"

	"github.com/lycaon/lycaon/internal/skills"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/invariants/promptrender"
)

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
	parts, err := promptrender.RenderSkill(t.Context(), sk, vars)
	contractcheck.FailErr(t, "render "+sk.Name, err)
	return parts
}
