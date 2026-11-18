package laws_test

import (
	"testing"

	"github.com/lycaon/lycaon/test/contract/laws"
	"github.com/lycaon/lycaon/test/contract/laws/catalogload"
)

const packs = "lycaon/config/packs/painted-wolf/"

var partialTree = map[string]string{
	packs + "platform/agents/prompts/coordinator.md":     "Intro\n{% include \"partials/shared.md\" %}\n",
	packs + "platform/agents/prompts/partials/shared.md": "Shared text\n",
}

// The family fails on each kind of drift, shown on rows that read the
// catalog through the corpus.
func TestCatalogLoadFailsClosed(t *testing.T) {
	t.Parallel()
	partials := rule(t, catalogload.Family, "prompt-partials-are-reachable")
	yamlSyntax := rule(t, catalogload.Family, "config-yaml-is-syntactically-valid")
	exempted := partials
	exempted.Exempt = []laws.Exemption{{Key: "partials/retired.md", Reason: "fixture"}}
	for name, tc := range map[string]struct {
		rule  laws.Rule
		files map[string]string
		want  outcome
	}{
		"clean": {rule: partials, files: partialTree, want: passes},
		"seeded violation": {rule: partials, files: with(partialTree, map[string]string{
			packs + "platform/agents/prompts/partials/orphan.md": "Nobody includes this\n",
		}), want: violates},
		"renamed anchor": {rule: partials, files: map[string]string{
			"lycaon/config/packs/other/agents/prompts/coordinator.md": "Intro\n",
		}, want: missesAnchor},
		"stale exemption": {rule: exempted, files: partialTree, want: staleExemption},
		"empty corpus":    {rule: yamlSyntax, files: with(partialTree, nil), want: fails},
		"unparsable file": {rule: yamlSyntax, files: with(partialTree, map[string]string{
			packs + "platform/host/broken.yaml": "key: [unclosed\n",
		}), want: violates},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			expect(t, laws.Evaluate(fixture(t, tc.files), tc.rule), tc.want, 0)
		})
	}
}

// Skill frontmatter carries documented keys only, and a skill without
// frontmatter fails.
func TestSkillFrontmatterRule(t *testing.T) {
	t.Parallel()
	frontmatter := rule(t, catalogload.Family, "stock-skill-frontmatter-is-documented")
	for name, tc := range map[string]struct {
		body string
		want outcome
	}{
		"documented keys":  {body: "---\nname: x\ndescription: y\n---\nBody\n", want: passes},
		"undocumented key": {body: "---\nname: x\noptional-tools: [a]\n---\nBody\n", want: violates},
		"no frontmatter":   {body: "Body only\n", want: violates},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			files := map[string]string{packs + "implement/skills/x/SKILL.md": tc.body}
			expect(t, laws.Evaluate(fixture(t, files), frontmatter), tc.want, 1)
		})
	}
}
