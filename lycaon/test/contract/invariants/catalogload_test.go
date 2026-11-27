package invariants_test

import (
	"github.com/lycaon/lycaon/test/contract/invariants/invariantstest"
	"testing"

	"github.com/lycaon/lycaon/test/contract/invariants"
	"github.com/lycaon/lycaon/test/contract/invariants/catalogload"
)

const packs = "lycaon/config/packs/painted-wolf/"

var partialTree = map[string]string{
	packs + "platform/agents/prompts/coordinator.md":     "Intro\n{% include \"partials/shared.md\" %}\n",
	packs + "platform/agents/prompts/partials/shared.md": "Shared text\n",
}

// The family invariantstest.Fails on each kind of drift, shown on rows that read the
// catalog through the corpus.
func TestCatalogLoadFailsClosed(t *testing.T) {
	t.Parallel()
	partials := invariantstest.Rule(t, catalogload.Family, "prompt-partials-are-reachable")
	yamlSyntax := invariantstest.Rule(t, catalogload.Family, "config-yaml-is-syntactically-valid")
	exempted := partials
	exempted.Exempt = []invariants.Exemption{{Key: "partials/retired.md", Reason: "fixture"}}
	for name, tc := range map[string]struct {
		rule  invariants.Invariant
		files map[string]string
		want  invariantstest.Outcome
	}{
		"clean": {rule: partials, files: partialTree, want: invariantstest.Passes},
		"seeded violation": {rule: partials, files: invariantstest.With(partialTree, map[string]string{
			packs + "platform/agents/prompts/partials/orphan.md": "Nobody includes this\n",
		}), want: invariantstest.Violates},
		"renamed anchor": {rule: partials, files: map[string]string{
			"lycaon/config/packs/other/agents/prompts/coordinator.md": "Intro\n",
		}, want: invariantstest.MissesAnchor},
		"stale exemption": {rule: exempted, files: partialTree, want: invariantstest.StaleExemption},
		"empty corpus":    {rule: yamlSyntax, files: invariantstest.With(partialTree, nil), want: invariantstest.Fails},
		"unparsable file": {rule: yamlSyntax, files: invariantstest.With(partialTree, map[string]string{
			packs + "platform/host/broken.yaml": "key: [unclosed\n",
		}), want: invariantstest.Violates},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			invariantstest.Expect(t, invariants.Evaluate(invariantstest.Fixture(t, tc.files), tc.rule), tc.want, 0)
		})
	}
}

// Skill frontmatter carries documented keys only, and a skill without
// frontmatter invariantstest.Fails.
func TestSkillFrontmatterRule(t *testing.T) {
	t.Parallel()
	frontmatter := invariantstest.Rule(t, catalogload.Family, "stock-skill-frontmatter-is-documented")
	for name, tc := range map[string]struct {
		body string
		want invariantstest.Outcome
	}{
		"documented keys":  {body: "---\nname: x\ndescription: y\n---\nBody\n", want: invariantstest.Passes},
		"undocumented key": {body: "---\nname: x\noptional-tools: [a]\n---\nBody\n", want: invariantstest.Violates},
		"no frontmatter":   {body: "Body only\n", want: invariantstest.Violates},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			files := map[string]string{packs + "implement/skills/x/SKILL.md": tc.body}
			invariantstest.Expect(t, invariants.Evaluate(invariantstest.Fixture(t, files), frontmatter), tc.want, 1)
		})
	}
}
