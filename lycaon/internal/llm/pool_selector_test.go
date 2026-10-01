package llm

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func testPolicy() ModelPolicy {
	return ModelPolicy{
		Coordinator: ModelRef{ProviderID: "openai", Model: "gpt-4o"},
		Lite:        ModelRef{ProviderID: "openai", Model: "gpt-4o-mini"},
		AgentPool: AgentPool{
			Selection: PoolSelectionRoundRobin,
			Models: []ModelRef{
				{ProviderID: "openai", Model: "gpt-4o-mini"},
				{ProviderID: "openai", Model: "gpt-4o"},
				{ProviderID: "ollama", Model: "llama3.1"},
			},
		},
	}
}

func TestPoolSelectorRoundRobin(t *testing.T) {
	s := NewPoolSelector(testPolicy())
	ctx := context.Background()

	first, err := s.SelectOne(ctx)
	testutil.FailErr(t, "s.SelectOne failed", err)
	second, err := s.SelectOne(ctx)
	testutil.FailErr(t, "s.SelectOne failed", err)
	if first.Model == second.Model {
		t.Fatalf("round_robin should advance: %q then %q", first.Model, second.Model)
	}
}

func TestPoolSelectorFirst(t *testing.T) {
	p := testPolicy()
	p.AgentPool.Selection = PoolSelectionFirst
	s := NewPoolSelector(p)

	sel, err := s.SelectOne(context.Background())
	testutil.FailErr(t, "s.SelectOne failed", err)
	if sel.Model != "gpt-4o-mini" {
		t.Fatalf("first = %q", sel.Model)
	}
}

func TestSelectGroupDiverseProviders(t *testing.T) {
	s := NewPoolSelector(testPolicy())
	group, err := s.SelectGroup(context.Background(), 2)
	testutil.FailErr(t, "s.SelectGroup failed", err)
	if len(group) != 2 {
		t.Fatalf("len = %d", len(group))
	}
	if group[0].ProviderID == group[1].ProviderID && group[0].Model == group[1].Model {
		t.Fatalf("expected diverse pick, got %+v and %+v", group[0], group[1])
	}
	if group[0].ProviderID == group[1].ProviderID {
		t.Fatalf("expected different providers when pool allows, got both %q", group[0].ProviderID)
	}
}

func TestSelectGroupHomogeneousPool(t *testing.T) {
	p := testPolicy()
	p.AgentPool.Models = []ModelRef{{ProviderID: "openai", Model: "gpt-4o-mini"}}
	s := NewPoolSelector(p)

	group, err := s.SelectGroup(context.Background(), 2)
	testutil.FailErr(t, "s.SelectGroup failed", err)
	if len(group) != 2 {
		t.Fatalf("len = %d", len(group))
	}
	if group[0].Model != group[1].Model {
		t.Fatalf("homogeneous pool should duplicate: %+v %+v", group[0], group[1])
	}
}

func TestSelectGroupHomogeneousPoolFillsRequestedCount(t *testing.T) {
	p := testPolicy()
	p.AgentPool.Models = []ModelRef{{ProviderID: "openai", Model: "gpt-4o-mini"}}
	s := NewPoolSelector(p)

	group, err := s.SelectGroup(t.Context(), 5)
	testutil.FailErr(t, "SelectGroup", err)
	if len(group) != 5 {
		t.Fatalf("len = %d, want 5", len(group))
	}
	for i, selection := range group {
		if selection.ProviderID != "openai" || selection.Model != "gpt-4o-mini" {
			t.Fatalf("group[%d] = %+v", i, selection)
		}
	}
}

func TestSelectGroupEmptyPoolFallsBackToCoordinator(t *testing.T) {
	p := testPolicy()
	p.AgentPool.Models = nil
	s := NewPoolSelector(p)

	group, err := s.SelectGroup(context.Background(), 2)
	testutil.FailErr(t, "s.SelectGroup failed", err)
	for _, sel := range group {
		if sel.Model != p.Coordinator.Model {
			t.Fatalf("expected coordinator fallback, got %+v", sel)
		}
	}
}
