package anthropic

import (
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelinfo"
)

func withThinkingRules(t *testing.T, rules []modelinfo.ThinkingRule) {
	t.Helper()
	modelinfo.SetThinkingRules(rules)
	t.Cleanup(func() { modelinfo.SetThinkingRules(nil) })
}
