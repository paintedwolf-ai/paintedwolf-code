package rules

import (
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestLoadBundledRuleConfigs(t *testing.T) {
	packs, err := LoadBundledRules()
	testutil.FailErr(t, "LoadBundledRules failed", err)
	if len(packs) < 4 {
		t.Fatalf("expected >=4 rule files, got %d", len(packs))
	}
	data, err := config.Read(config.SessionPostures)
	testutil.FailErr(t, "read session postures", err)
	var postureFile struct {
		Postures map[string]struct {
			Rules []string `yaml:"rules"`
		} `yaml:"postures"`
	}
	if err := yaml.Unmarshal(data, &postureFile); err != nil {
		testutil.FailErr(t, "unmarshal YAML document", err)
	}
	for id, spec := range postureFile.Postures {
		for _, p := range spec.Rules {
			key := NormalizeRulesPath(p)
			if _, ok := packs[key]; !ok {
				t.Fatalf("posture %q references missing pack %q", id, key)
			}
		}
	}
}
