package contract

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/rules"
	sessionposture "github.com/lycaon/lycaon/internal/session/posture"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// postureRulesFile maps rules/*.yaml basename → expected posture_is value.
var postureRulesFile = map[string]string{
	"spec.yaml":        "spec",
	"build.yaml":       "build",
	"orchestrate.yaml": "orchestrate",
	"vet.yaml":         "vet",
}

func TestPostureBundledRulesDeclareMatchingPosture(t *testing.T) {
	t.Parallel()
	for file, wantPosture := range postureRulesFile {
		cfg, err := rules.LoadRulesConfig(config.PostureRulesDir.Join(file))
		if err != nil {
			t.Fatalf("load %s: %v", file, err)
		}
		if len(cfg.Rules) == 0 {
			t.Fatalf("%s: expected rules", file)
		}
		hasPostureRule := false
		for _, rule := range cfg.Rules {
			raw, ok := rule.When["posture_is"]
			if !ok {
				if _, unresolved := rule.When["posture_unresolved"]; unresolved && file == "spec.yaml" {
					continue
				}
				continue
			}
			hasPostureRule = true
			got, _ := raw.(string)
			if got != wantPosture {
				t.Errorf("%s rule %q posture_is = %q want %q", file, rule.ID, got, wantPosture)
			}
			if !sessionposture.ValidSessionPosture(got) {
				t.Errorf("%s rule %q invalid posture_is %q", file, rule.ID, got)
			}
		}
		if !hasPostureRule {
			t.Errorf("%s: no rule declares posture_is:%s", file, wantPosture)
		}
	}
}

func TestPostureRuleDenyCodesUsePostureNamespace(t *testing.T) {
	t.Parallel()
	hintCfg, err := guidance.LoadHintConfigStock()
	contractcheck.FailErr(t, "load hint registry", err)
	entries, err := config.List(config.PostureRulesDir)
	contractcheck.FailErr(t, "read directory entries", err)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		cfg, err := rules.LoadRulesConfig(config.PostureRulesDir.Join(e.Name()))
		contractcheck.FailErr(t, "load rules config YAML", err)
		for _, rule := range cfg.Rules {
			deny, ok := rule.Then["deny"].(map[string]any)
			if !ok {
				continue
			}
			code, _ := deny["code"].(string)
			if code == "" {
				continue
			}
			if _, known := hintCfg.HintCodes[code]; known {
				continue
			}
			if !isAllowedDenyCode(code) {
				t.Errorf("%s rule %q deny code %q not in hint registry and not posture-namespaced", e.Name(), rule.ID, code)
			}
		}
	}
}

func isAllowedDenyCode(code string) bool {
	if code == "DISALLOWED_AGENT" {
		return true
	}
	if strings.HasPrefix(code, "WORKFLOW_") {
		return true
	}
	return strings.Contains(code, "_POSTURE_")
}
