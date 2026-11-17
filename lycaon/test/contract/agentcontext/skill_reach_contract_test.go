package contract

import (
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/test/contract/laws/promptrender"

	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/skills"
	"github.com/lycaon/lycaon/internal/toolvocab"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func checkSkillReach(t *testing.T, catalog *toolvocab.Catalog, sk skills.Skill, reader string, corpus string, held map[string]bool) {
	t.Helper()
	if names := toolvocab.UnreachableNames(catalog, corpus, held); len(names) > 0 {
		t.Errorf("skill %q as read by %s names %s, which that reader cannot reach:\n%s",
			sk.Name, reader, strings.Join(names, ", "), linesNaming(corpus, names))
	}
}

// Every stock skill names only tools and command programs its reader can reach
// (INV-SK-01): coordinator surfaces across root counts, and every worker
// profile across branch and git states. Retired names count: a skill must not
// teach what no longer exists.
func TestStockSkillsNameOnlyReachableToolsAndPrograms(t *testing.T) {
	catalog, _ := promptNameCatalog(t)
	forEachStockSkillRender(t, func(t *testing.T, sk skills.Skill, readerLabel string, reader prompts.Reader, _ map[string]any, parts []promptrender.Text) {
		corpus := make([]string, len(parts))
		for i, part := range parts {
			corpus[i] = part.Text
		}
		checkSkillReach(t, catalog, sk, readerLabel, strings.Join(corpus, "\n"), reader.Held())
	})
}

// TestSurveyRepoAliasAdmittedAtRenderLevel pins the PC09 alias end to end
// through the render-level reach gate: rendered copy may spell the repo-map
// form as `survey-repo`, the gate resolves it through the survey_repo form
// tool, and the alias stays gated on command and on survey_repo exactly like
// the canonical spelling.
func TestSurveyRepoAliasAdmittedAtRenderLevel(t *testing.T) {
	catalog, _ := promptNameCatalog(t)
	corpus := "Map the tree with `survey-repo` before reading files.\n"
	held := map[string]bool{"command": true, "survey_repo": true}
	if names := toolvocab.UnreachableNames(catalog, corpus, held); len(names) > 0 {
		t.Fatalf("survey-repo alias flagged unreachable for a command+survey_repo reader: %v", names)
	}
	for _, incomplete := range []map[string]bool{
		{"command": true},
		{"survey_repo": true},
	} {
		if names := toolvocab.UnreachableNames(catalog, corpus, incomplete); !slices.Contains(names, "survey-repo") {
			t.Fatalf("survey-repo must stay gated on command and survey_repo; held %v admitted it (unreachable = %v)", incomplete, names)
		}
	}
}

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
			check(t, sr.Skill, sr.Reader, sr.State, prompts.SkillVars(sr.State), sr.Parts)
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
