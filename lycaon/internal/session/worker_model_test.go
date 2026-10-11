package session

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

type failingWorkerModelStore struct {
	*store.Memory
	err error
}

func (s *failingWorkerModelStore) UpdateSession(context.Context, string, func(*api.Session)) error {
	return s.err
}

func TestWorkerModelSaveFailureRemovesPartialChild(t *testing.T) {
	st := &failingWorkerModelStore{Memory: store.NewMemory(), err: errors.New("model save failed")}
	policy := llm.NewInMemoryPolicyStore(llm.ModelPolicy{
		Coordinator: llm.ModelRef{ProviderID: "fixture", Model: "fixture"},
	})
	manager := NewHost(st, Models{Client: nil, Provider: &llm.Service{Router: llm.NewStaticModelRouter(policy)}, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	parent, err := st.Create(t.Context(), api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create worker parent", err)
	child, err := manager.Workers.SpawnChild(t.Context(), parent.ID, api.SpawnChildRequest{AgentType: "implement"})
	if child != nil || !errors.Is(err, st.err) {
		t.Fatalf("spawn = %+v, %v; want no child and save failure", child, err)
	}
	sessions, err := st.List(t.Context())
	testutil.FailErr(t, "list after failed assignment", err)
	if len(sessions) != 1 || sessions[0].ID != parent.ID {
		t.Fatalf("failed assignment retained a partial worker: %+v", sessions)
	}
}

func TestSpawnedWorkerKeepsPoolAssignmentAfterReload(t *testing.T) {
	database := testdbfixture.Open(t, "worker-model.db")
	testdbseed.InsertProjectRoot(t, database, testdbseed.DefaultProjectID, t.TempDir())
	st := store.NewSQL(database)
	policy := llm.NewInMemoryPolicyStore(llm.ModelPolicy{
		Coordinator: llm.ModelRef{ProviderID: "coordinator", Model: "coordinator-model"},
		AgentPool: llm.AgentPool{Selection: llm.PoolSelectionRoundRobin, Models: []llm.ModelRef{
			{ProviderID: "first-provider", Model: "first-model"},
			{ProviderID: "second-provider", Model: "second-model"},
		}},
	})
	manager := NewHost(st, Models{Client: nil, Provider: &llm.Service{Policy: policy, Router: llm.NewStaticModelRouter(policy)}, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	parent, err := st.Create(t.Context(), api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create worker parent", err)
	for i, provider := range []string{"first-provider", "second-provider", "first-provider"} {
		child, err := manager.Workers.SpawnChild(t.Context(), parent.ID, api.SpawnChildRequest{AgentType: "implement", Prompt: "inspect the fixture"})
		testutil.FailErr(t, "spawn assigned worker", err)
		if child.ProviderID != provider || child.Model == "" {
			t.Fatalf("worker %d assignment = %s/%s, want provider %s", i, child.ProviderID, child.Model, provider)
		}
		loaded, err := store.NewSQL(database).Get(t.Context(), child.ID)
		testutil.FailErr(t, "reload assigned worker", err)
		assertWorkerRoutingStable(t, llm.NewStaticModelRouter(policy), loaded, child)
	}
}

func assertWorkerRoutingStable(t *testing.T, router *llm.StaticModelRouter, loaded, child *api.Session) {
	t.Helper()
	for range 4 {
		got, err := router.WithOverlayRoots(nil).ResolveSession(t.Context(), loaded)
		testutil.FailErr(t, "resolve reloaded worker", err)
		if got.ProviderID != child.ProviderID || got.Model != child.Model {
			t.Fatalf("worker assignment changed after reload: %+v, want %s/%s", got, child.ProviderID, child.Model)
		}
	}
}
