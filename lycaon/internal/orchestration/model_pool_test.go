package orchestration

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestSingleModelPool(t *testing.T) {
	s := llm.NewPoolSelector(llm.ModelPolicy{
		Coordinator: llm.ModelRef{ProviderID: "openai", Model: "solo-model"},
	})
	group, err := s.SelectGroup(context.Background(), 2)
	testutil.FailErr(t, "s.SelectGroup failed", err)
	if len(group) != 2 {
		t.Fatalf("group len = %d", len(group))
	}
	if group[0].Model != group[1].Model || group[0].ProviderID != group[1].ProviderID {
		t.Fatalf("empty pool should repeat coordinator model: %+v", group)
	}
}

func TestStaticModelGroupSelectorSelectGroup(t *testing.T) {
	tmp := t.TempDir()
	policyPath := filepath.Join(tmp, "model-policy.yaml")
	const yaml = `coordinator:
  provider_id: openai
  model: gpt-4o-mini

lite:
  provider_id: ""
  model: ""

agent_pool:
  selection: round_robin
  models:
    - provider_id: openai
      model: gpt-4o-mini
    - provider_id: openai
      model: gpt-4o
`
	if err := os.WriteFile(policyPath, []byte(yaml), 0o600); err != nil {
		t.Fatalf("write policy: %v", err)
	}
	policy, err := llm.NewPolicyStoreAt(filepath.Join(tmp, "global.yaml"))
	testutil.FailErr(t, "llm.NewPolicyStoreAt failed", err)
	selector := &StaticModelGroupSelector{Router: llm.NewStaticModelRouter(policy)}
	group, err := selector.SelectGroup(context.Background(), "/tmp/project", 2)
	testutil.FailErr(t, "selector.SelectGroup failed", err)
	if len(group) != 2 {
		t.Fatalf("assignments = %d want 2", len(group))
	}
}
