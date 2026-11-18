package catalogload

import (
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/boolexpr"
	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/rules"
	sessionposture "github.com/lycaon/lycaon/internal/session/posture"
	"github.com/lycaon/lycaon/internal/vocabulary"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/test/contract/laws"
)

// postureRules returns every bundled posture rule file, loaded.
func postureRules(r *laws.Run) map[string]*rules.RulesConfig {
	out := map[string]*rules.RulesConfig{}
	for _, rel := range r.Glob("lycaon/config/packs/painted-wolf/platform/host/posture-rules/*.yaml") {
		name := rel[strings.LastIndex(rel, "/")+1:]
		cfg, err := rules.LoadRulesConfig(config.PostureRulesDir.Join(name))
		r.Must(err)
		out[name] = cfg
	}
	return out
}

// ruleConditions returns the condition registry with the rule conditions.
func ruleConditions(r *laws.Run) *conditions.ConditionRegistry {
	reg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{})
	r.Must(err)
	r.Must(rules.RegisterRuleConditions(reg))
	return reg
}

// knownWhenKey reports a when key the rules engine fixes or a condition implements.
func knownWhenKey(reg *conditions.ConditionRegistry, key string) bool {
	for _, k := range rules.RuleWhenKeys() {
		if k == key {
			return true
		}
	}
	return reg.Has(key)
}

// legacyPostures are retired posture values.
var legacyPostures = []string{"plan", "implement", "delegation", "security"}

var rulesConfig = []laws.Rule{{
	// A posture rule's when keys are implemented and its posture is valid.
	Name:    "rule-yaml-when-keys-are-implemented",
	Anchors: []laws.Anchor{laws.Func("internal/rules", "RegisterRuleConditions")},
	Check: func(r *laws.Run) {
		reg := ruleConditions(r)
		for name, cfg := range postureRules(r) {
			for _, rule := range cfg.Rules {
				for key, raw := range rule.When {
					if !knownWhenKey(reg, key) {
						r.Report("%s rule %s: unknown when key %s", name, rule.ID, key)
					}
					if val, _ := raw.(string); key == "posture_is" && !sessionposture.ValidSessionPosture(val) {
						r.Report("%s rule %s: invalid posture_is %q", name, rule.ID, val)
					}
				}
			}
		}
	},
}, {
	// A rule's canonical when string parses, stays in the whitelist and
	// names only known keys.
	Name:    "rule-canonical-when-matches-the-when-keys",
	Anchors: []laws.Anchor{laws.Func("internal/rules", "CanonicalWhenString"), laws.Func("internal/boolexpr", "ValidateWhitelist")},
	Check: func(r *laws.Run) {
		reg := ruleConditions(r)
		for name, cfg := range postureRules(r) {
			for _, rule := range cfg.Rules {
				canon, err := rules.CanonicalWhenString(rule.When)
				if err != nil {
					r.Report("%s rule %s: %v", name, rule.ID, err)
					continue
				}
				tree, err := boolexpr.Parse(canon)
				if err != nil {
					r.Report("%s rule %s: parse %q: %v", name, rule.ID, canon, err)
					continue
				}
				if err := boolexpr.ValidateWhitelist(tree); err != nil {
					r.Report("%s rule %s: whitelist: %v", name, rule.ID, err)
				}
				for _, id := range boolexpr.CollectIdents(tree) {
					if !strings.HasPrefix(id, "posture_is_") && !knownWhenKey(reg, id) {
						r.Report("%s rule %s: canonical %q names the unknown key %s", name, rule.ID, canon, id)
					}
				}
			}
		}
	},
}, {
	// Workflows reference only implemented plan predicates.
	Name:    "bundled-workflows-reference-implemented-predicates",
	Anchors: []laws.Anchor{laws.Func("internal/vocabulary", "ValidateBundled")},
	Check: func(r *laws.Run) {
		manifests, err := workflowdef.RegistryFromDirs("")
		r.Must(err)
		ruleConfigs, err := rules.LoadBundledRuleConfigs()
		r.Must(err)
		for _, diag := range vocabulary.ValidateBundled(ruleConditions(r), manifests, ruleConfigs) {
			r.Report("%v", diag)
		}
	},
}, {
	// A workflow agent declares its tools policy.
	Name:    "workflow-agents-declare-a-tools-policy",
	Anchors: []laws.Anchor{laws.Dir("lycaon/config/packs/painted-wolf")},
	Check: func(r *laws.Run) {
		seen := 0
		for _, f := range r.Tree("lycaon/config/packs/painted-wolf", ".yaml") {
			if !strings.HasSuffix(f.Rel, "/workflow.yaml") {
				continue
			}
			seen++
			var doc struct {
				Agents []struct {
					ID    string `yaml:"id"`
					Tools string `yaml:"tools"`
				} `yaml:"agents"`
			}
			r.Must(yaml.Unmarshal(f.Bytes(), &doc))
			for _, agent := range doc.Agents {
				if strings.TrimSpace(agent.Tools) == "" {
					r.Report("%s: agent %s declares no tools policy", f.Rel, agent.ID)
				}
			}
		}
		r.Expect(seen, "workflow manifests")
	},
}, {
	// Manifest postures are current session postures, never phase ids.
	Name:    "bundled-manifest-postures-are-valid",
	Anchors: []laws.Anchor{laws.Func("internal/session/posture", "ValidSessionPosture")},
	Check: func(r *laws.Run) {
		reg, err := workflowdef.RegistryFromDirs("")
		r.Must(err)
		check := func(key, field, value string, m workflowdef.Manifest) {
			if !sessionposture.ValidSessionPosture(value) || contains(legacyPostures, value) {
				r.Report("%s %s = %q is not a current session posture", key, field, value)
			}
			if contains(m.Phases, value) {
				r.Report("%s %s = %q conflates a posture with a phase id", key, field, value)
			}
		}
		for key, m := range reg.All() {
			if ip := strings.TrimSpace(m.InitialPosture); ip != "" {
				check(key, "initial_posture", ip, m)
				if got := m.Summary().InitialPosture; got != ip {
					r.Report("%s: summary initial_posture %q differs from the manifest's %q", key, got, ip)
				}
			}
			for _, def := range m.PhaseDefs {
				if sp := strings.TrimSpace(def.OnEnter.SetPosture); sp != "" {
					check(key, "on_enter.set_posture on "+def.ID, sp, m)
				}
			}
		}
	},
}, {
	// Every gate leaf a bundled manifest uses is a registered condition, and
	// every complete_when and gate is in the workflow vocabulary.
	Name:    "bundled-manifest-vocabulary-is-registered",
	Anchors: []laws.Anchor{laws.Func("internal/workflow/definition", "CollectManifestVocabulary")},
	Check: func(r *laws.Run) {
		reg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{})
		r.Must(err)
		manifests, err := workflowdef.RegistryFromDirs("")
		r.Must(err)
		for key, m := range manifests.All() {
			completeWhen, gates := workflowdef.CollectManifestVocabulary(m)
			for _, cw := range completeWhen {
				if !workflowdef.IsKnownCompleteWhen(cw) {
					r.Report("%s: unknown complete_when %q", key, cw)
				}
			}
			for _, leaf := range gates {
				if !workflowdef.IsKnownGateLeaf(leaf) {
					r.Report("%s: unknown gate leaf %q", key, leaf)
				}
				if !reg.Has(leaf) {
					r.Report("%s: gate %q is not a registered condition", key, leaf)
				}
			}
		}
	},
}}
