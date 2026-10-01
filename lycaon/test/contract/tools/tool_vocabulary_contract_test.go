package contract

import (
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/agentdef"
	"github.com/lycaon/lycaon/internal/anchorcatalog"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/skills"
	"github.com/lycaon/lycaon/internal/toolvocab"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// vocabularyFixture assembles the same inputs boot audits.
type vocabularyFixture struct {
	catalog  *toolvocab.Catalog
	profiles []sandbox.ToolProfile
	agents   []toolvocab.AgentSurface
	surfaces toolvocab.Surfaces
	rules    *oar.RuleSet
	skills   []skills.Skill
}

func loadVocabularyFixture(t *testing.T) *vocabularyFixture {
	t.Helper()
	root := contractcheck.RepoRoot(t)
	configRoot := filepath.Join(root, "lycaon")
	catalogPath := filepath.Join(configRoot, "config", "packs", "painted-wolf",
		"platform", "host", "anchors", "catalog.yaml")
	contractcheck.FailErr(t, "install anchor catalog", anchorcatalog.InstallFile(catalogPath))

	eff := contractcheck.StockCatalog(t)
	profiles, err := sandbox.LoadToolProfilesWithCatalog(eff)
	contractcheck.FailErr(t, "load tool profiles", err)

	schemas, _, err := extpacks.LoadEffectiveToolSchemas(eff)
	contractcheck.FailErr(t, "load tool schemas", err)

	// Stock content is one authority, so the fixture leaves provenance empty
	// and every remedy is judged strictly.
	catalog, err := toolvocab.NewCatalog(schemas, profiles, toolvocab.Provenance{})
	contractcheck.FailErr(t, "toolvocab.NewCatalog", err)

	agentProfiles, err := agentdef.LoadEffectiveWithCatalog(eff)
	contractcheck.FailErr(t, "load agent profiles", err)
	agents, surfaces, err := toolvocab.AgentSurfaces(agentProfiles, profiles)
	contractcheck.FailErr(t, "toolvocab.AgentSurfaces", err)

	loader, err := oar.NewLoader(filepath.Join(root, "schemas"))
	contractcheck.FailErr(t, "oar loader", err)
	rules, err := loader.LoadEffectivePolicyWithCatalog(eff)
	contractcheck.FailErr(t, "load effective policy", err)

	stockSkills, _ := extpacks.LoadEffectiveSkills(eff)
	return &vocabularyFixture{
		catalog: catalog, profiles: profiles, agents: agents,
		surfaces: surfaces, rules: rules, skills: stockSkills,
	}
}

// A profile entry that resolves to nothing is not inert: an allow key grants a
// tool that never arrives, and a deny that matches nothing grants.
func TestProfileToolNamesResolve(t *testing.T) {
	fx := loadVocabularyFixture(t)
	contractcheck.FailErr(t, "resolve every profile allow key and deny pattern",
		toolvocab.ValidateProfiles(fx.catalog, fx.profiles))
}

// A selector naming a tool that does not exist can never match, and a Fix naming
// a tool its reader lacks leaves a blocked model no exit.
func TestPolicyToolVocabularyResolvesAndRemediesAreReachable(t *testing.T) {
	fx := loadVocabularyFixture(t)
	notes, err := toolvocab.CheckRules(fx.catalog, fx.surfaces, fx.rules)
	contractcheck.FailErr(t, "resolve policy tool names and remedy reachability", err)
	if len(notes) > 0 {
		t.Fatalf("stock is one authority, so it can produce no cross-author notes: %v", notes)
	}
}

// A skill offered to an agent whose profile lacks its tools rejects on contact.
func TestSkillToolVocabularyMatchesGrantedProfiles(t *testing.T) {
	fx := loadVocabularyFixture(t)
	contractcheck.FailErr(t, "resolve skill tool names against the profiles they are offered to",
		toolvocab.ValidateSkills(fx.catalog, fx.agents, fx.skills))
}
