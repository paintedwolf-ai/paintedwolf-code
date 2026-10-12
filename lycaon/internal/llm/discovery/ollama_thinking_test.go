package discovery

import (
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelinfo"
)

func TestOllamaThinkingMetadata(t *testing.T) {
	modelinfo.SetThinkingRules([]modelinfo.ThinkingRule{{Match: []string{"levels"}, Style: "effort_levels",
		Controls: &modelinfo.ThinkingCapabilities{State: "supported", Efforts: []string{"low", "high"}},
	}})
	t.Cleanup(func() { modelinfo.SetThinkingRules(nil) })
	if got := ollamaThinking(nil, "unknown"); got != nil {
		t.Fatalf("missing capabilities became evidence: %+v", got)
	}
	for _, capabilities := range [][]string{{}, {"completion", "tools"}} {
		if got := ollamaThinking(capabilities, "levels"); got.State != "unsupported" {
			t.Fatalf("inventory refusal lost to family rule: %+v", got)
		}
	}
	got := ollamaThinking([]string{"completion", "thinking"}, "new-model")
	if got.State != "supported" || !got.CanEnable || !got.CanDisable || len(got.Efforts) != 0 {
		t.Fatalf("native boolean controls: %+v", got)
	}
	got = ollamaThinking([]string{"completion", "thinking"}, "levels")
	if len(got.Efforts) != 2 || got.Efforts[1] != "high" {
		t.Fatalf("native level controls lost: %+v", got)
	}
	got.Efforts[0] = "mutated"
	if next := ollamaThinking([]string{"thinking"}, "levels"); next.Efforts[0] != "low" {
		t.Fatal("discovery mutated shared family controls")
	}
}
