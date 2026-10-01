package llm

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

// fakeModelPolicy isolates routing tests from bundled settings.
var fakeModelPolicy = ModelPolicy{
	Coordinator: ModelRef{ProviderID: "prov-a", Model: "model-x"},
	Lite:        ModelRef{ProviderID: "prov-a", Model: "model-y"},
	AgentPool: AgentPool{
		Selection: PoolSelectionRoundRobin,
		Models: []ModelRef{
			{ProviderID: "prov-a", Model: "model-x"},
			{ProviderID: "prov-a", Model: "model-y"},
		},
	},
}

func testRouter(t *testing.T) *StaticModelRouter {
	t.Helper()
	return NewStaticModelRouter(NewInMemoryPolicyStore(fakeModelPolicy))
}

func TestResolveSessionUsesCoordinatorForParent(t *testing.T) {
	r := testRouter(t)
	sel, err := r.ResolveSession(context.Background(), &api.Session{WorkspacePath: "/tmp/p"})
	testutil.FailErr(t, "r.ResolveSession failed", err)
	if sel.Role != ModelRoleCoordinator {
		t.Fatalf("role = %q want coordinator", sel.Role)
	}
	if sel.ProviderID == "" || sel.Model == "" {
		t.Fatalf("coordinator selection empty: %+v", sel)
	}
}

func TestResolveSessionUsesPoolForWorkerChild(t *testing.T) {
	r := testRouter(t)
	sel, err := r.ResolveSession(context.Background(), &api.Session{
		WorkspacePath:   "/tmp/p",
		ParentSessionID: "parent-1",
	})
	testutil.FailErr(t, "r.ResolveSession failed", err)
	if sel.Role != ModelRolePool {
		t.Fatalf("role = %q want pool", sel.Role)
	}
	if sel.ProviderID == "" || sel.Model == "" {
		t.Fatalf("pool selection empty: %+v", sel)
	}
}

func TestResolveSessionHonorsExplicitOverride(t *testing.T) {
	r := testRouter(t)
	sel, err := r.ResolveSession(context.Background(), &api.Session{
		ProviderID: "custom",
		Model:      "custom-model",
	})
	testutil.FailErr(t, "r.ResolveSession failed", err)
	if sel.ProviderID != "custom" || sel.Model != "custom-model" {
		t.Fatalf("override = %+v", sel)
	}
}

func TestLiteUsesCompactModelPolicy(t *testing.T) {
	r := testRouter(t)
	sel, err := r.Lite(context.Background())
	testutil.FailErr(t, "r.Lite failed", err)
	if sel.Role != ModelRoleLite {
		t.Fatalf("role = %q want lite", sel.Role)
	}
	p, err := r.effectivePolicy()
	testutil.FailErr(t, "r.effectivePolicy failed", err)
	want := SummarizerRef(p)
	if sel.ProviderID != want.ProviderID || sel.Model != want.Model {
		t.Fatalf("lite = %+v SummarizerRef = %+v", sel, want)
	}
}

func TestLiteFallsBackToCoordinatorWhenUnset(t *testing.T) {
	policy := NewInMemoryPolicyStore(ModelPolicy{
		Coordinator: ModelRef{ProviderID: "prov-a", Model: "model-x"},
		Lite:        ModelRef{},
		AgentPool: AgentPool{
			Selection: PoolSelectionFirst,
			Models:    []ModelRef{{ProviderID: "prov-a", Model: "model-x"}},
		},
	})
	r := NewStaticModelRouter(policy)
	sel, err := r.Lite(context.Background())
	testutil.FailErr(t, "r.Lite failed", err)
	p, err := r.effectivePolicy()
	testutil.FailErr(t, "r.effectivePolicy failed", err)
	if sel.ProviderID != p.Coordinator.ProviderID || sel.Model != p.Coordinator.Model {
		t.Fatalf("lite = %+v coordinator = %+v", sel, p.Coordinator)
	}
}

func TestSelectUsesPool(t *testing.T) {
	r := testRouter(t)
	sel, err := r.Select(context.Background())
	testutil.FailErr(t, "r.Select failed", err)
	if sel.Role != ModelRolePool {
		t.Fatalf("role = %q want pool", sel.Role)
	}
}

func TestSelectGroupReturnsPoolModels(t *testing.T) {
	r := testRouter(t)
	group, err := r.SelectGroup(context.Background(), 2)
	testutil.FailErr(t, "r.SelectGroup failed", err)
	if len(group) != 2 {
		t.Fatalf("len = %d", len(group))
	}
	for i, sel := range group {
		if sel.Role != ModelRolePool {
			t.Fatalf("group[%d] role = %q want pool", i, sel.Role)
		}
	}
}
