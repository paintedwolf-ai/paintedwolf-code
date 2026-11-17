package catalogload

import (
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/oarcore"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/test/contract/laws"
)

const policyGlob = "lycaon/config/packs/painted-wolf/*/policy/*.yaml"

var copyFields = []string{"what", "cause", "why", "fix", "instead", "message"}

var policyCopy = []laws.Rule{{
	// Rule copy uses only the frozen copy constructs and declared facts.
	Name:    "stock-policy-copy-uses-frozen-bindings",
	Anchors: []laws.Anchor{laws.Func("internal/oarcore", "ParseCopy"), laws.Func("internal/oar", "DeclaresCopyFact")},
	Check: func(r *laws.Run) {
		rules := 0
		for _, rel := range r.Glob(policyGlob) {
			var doc map[string]any
			r.Must(yaml.Unmarshal(r.Bytes(rel), &doc))
			if _, isRule := doc["spec_version"]; !isRule {
				continue
			}
			rules++
			for _, key := range copyFields {
				field, _ := doc[key].(string)
				if !strings.Contains(field, "{{") && !strings.Contains(field, "{%") {
					continue
				}
				bindings, err := oarcore.ParseCopy(field)
				if err != nil {
					r.Report("%s %s: illegal copy construct: %v", rel, key, err)
					continue
				}
				for _, b := range bindings {
					if !oar.DeclaresCopyFact(b.Name) {
						r.Report("%s %s: undeclared copy fact %s", rel, key, b.Name)
					}
				}
			}
		}
		r.Expect(rules, "policy rules")
	},
}, {
	// A shipped policy unit is reachable: a hint ships copy beyond its bare
	// code, and only a deprecated unit with a successor may never apply.
	Name:    "shipped-policy-units-are-reachable",
	Anchors: []laws.Anchor{laws.Func("internal/guidance", "NormalizeRejectCodeView")},
	Check: func(r *laws.Run) {
		for _, rel := range r.Glob(policyGlob) {
			var unit struct {
				ID        string `yaml:"id"`
				Kind      string `yaml:"kind"`
				When      string `yaml:"when"`
				Status    string `yaml:"x-paintedwolf-status"`
				Successor string `yaml:"x-paintedwolf-successor"`
			}
			raw := r.Bytes(rel)
			r.Must(yaml.Unmarshal(raw, &unit))
			if unit.Kind == "hint" {
				var entry guidance.HintEntry
				r.Must(yaml.Unmarshal(raw, &entry))
				view := guidance.NormalizeRejectCodeView(unit.ID, entry)
				if view.What == unit.ID && view.Cause == "" && view.Why == "" && view.Fix == "" && view.Instead == "" {
					r.Report("%s: hint ships no copy beyond its bare code", rel)
				}
			}
			retired := strings.TrimSpace(unit.Status) == "deprecated" && strings.TrimSpace(unit.Successor) != ""
			if strings.TrimSpace(unit.When) == "false" && !retired {
				r.Report("%s: when 'false' is unreachable; the agent would receive the bare code %s", rel, unit.ID)
			}
		}
	},
}, {
	// Skill bodies name no spawnable agent: the spawn roster is injected
	// live, and a copied id drifts into DISALLOWED_AGENT.
	Name:    "bundled-skills-name-no-spawnable-agent",
	Anchors: []laws.Anchor{laws.Func("internal/spawn", "AmbientAllowedAgents")},
	Check: func(r *laws.Run) {
		ids := spawn.AmbientAllowedAgents()
		r.Expect(len(ids), "spawnable agents")
		packs, err := extpacks.DiscoverStock()
		r.Must(err)
		dirs := extpacks.KindDirs(packs, "skills")
		r.Expect(len(dirs), "stock skill directories")
		skills := 0
		for _, dir := range dirs {
			entries, err := dir.List()
			r.Must(err)
			for _, entry := range entries {
				if !entry.IsDir() {
					continue
				}
				raw, err := dir.Join(entry.Name(), "SKILL.md").Read()
				if err != nil {
					continue
				}
				skills++
				for _, id := range ids {
					word := regexp.MustCompile(`(?i)(^|[^a-z0-9_-])` + regexp.QuoteMeta(id) + `([^a-z0-9_-]|$)`)
					for _, line := range strings.Split(string(raw), "\n") {
						if word.MatchString(line) {
							r.Report("skill %s names agent %s: %s", entry.Name(), id, strings.TrimSpace(line))
						}
					}
				}
			}
		}
		r.Expect(skills, "stock skills")
	},
}}
