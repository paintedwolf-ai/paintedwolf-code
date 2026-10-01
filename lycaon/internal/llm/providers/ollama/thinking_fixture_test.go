package ollama

import (
	"testing"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/testutil"
	"gopkg.in/yaml.v3"
)

func withThinkingRules(t *testing.T, rules []modelinfo.ThinkingRule) {
	t.Helper()
	modelinfo.SetThinkingRules(rules)
	t.Cleanup(func() { modelinfo.SetThinkingRules(nil) })
}
func bundledThinkingRules(t *testing.T) []modelinfo.ThinkingRule {
	t.Helper()
	data, err := config.Read(config.Providers)
	testutil.FailErr(t, "read bundled thinking rules", err)
	var cfg struct {
		ModelThinking []modelinfo.ThinkingRule `yaml:"model_thinking"`
	}
	testutil.FailErr(t, "decode bundled thinking rules", yaml.Unmarshal(data, &cfg))
	return cfg.ModelThinking
}
