package orchestration_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/lycaon/lycaon/internal/delegation"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workspace"
	"github.com/lycaon/lycaon/pkg/api"
)

type stubWorkspaceBinder struct {
	created      []*workspace.Binding
	destroyed    []*workspace.Binding
	failCreateOn int
	creates      int
}

func (s *stubWorkspaceBinder) CreateWorkerWorkspace(_ context.Context, _, legID string) (*workspace.Binding, error) {
	s.creates++
	if s.failCreateOn == s.creates {
		return nil, fmt.Errorf("create worker workspace forced failure")
	}
	b := &workspace.Binding{ID: legID, Root: "/tmp/" + legID}
	s.created = append(s.created, b)
	return b, nil
}

func (s *stubWorkspaceBinder) DestroyWorkerWorkspace(_ context.Context, b *workspace.Binding) error {
	s.destroyed = append(s.destroyed, b)
	return nil
}

// failingUpdateStore forces the UpdateLeg error path while every other store
// operation behaves like the memory store.
type failingUpdateStore struct {
	*delegation.MemoryStore
}

func (s *failingUpdateStore) UpdateLeg(context.Context, api.Leg) error {
	return errors.New("update leg forced failure")
}

func TestBindLegWorkspacesSharedSkips(t *testing.T) {
	store := delegation.NewMemoryStore()
	orch := orchestration.NewOrchestratorImpl(orchestration.OrchestratorDeps{Store: store})
	bindings, err := orch.BindLegWorkspacesIfIsolatedForTest(context.Background(), orchestration.TopologySpec{WorkspaceMode: orchestration.WorkspaceShared}, "/tmp", "dep", []string{"leg-1"})
	if err != nil || len(bindings) != 0 {
		t.Fatalf("bindings = %v err=%v", bindings, err)
	}
}

func TestBindLegWorkspacesIsolatedUpdatesLeg(t *testing.T) {
	binder := &stubWorkspaceBinder{}
	store := delegation.NewMemoryStore()
	ctx := context.Background()
	leg := api.Leg{ID: "leg-1", Title: "probe-0", Status: api.LegStatusPending}
	dir := t.TempDir()
	del := api.Delegation{ProjectID: "proj-1", WorkspacePath: dir, Task: "t", Status: "active"}
	created, err := store.Create(ctx, del, "sess", []api.Leg{leg})
	testutil.FailErr(t, "create session in store", err)
	orch := orchestration.NewOrchestratorImpl(orchestration.OrchestratorDeps{Store: store, Workspaces: binder})
	bindings, err := orch.BindLegWorkspacesIfIsolatedForTest(ctx, orchestration.TopologySpec{
		Pattern:       orchestration.TopologyPack,
		WorkspaceMode: orchestration.WorkspaceIsolated,
	}, del.WorkspacePath, created.ID, []string{"leg-1"})
	testutil.FailErr(t, "orch.BindLegWorkspacesIfIsolatedForTest failed", err)
	if len(bindings) != 1 {
		t.Fatalf("bindings = %d", len(bindings))
	}
	got, err := store.GetLeg(ctx, created.ID, "leg-1")
	testutil.FailErr(t, "store.GetLeg failed", err)
	if got.WorkspaceRoot == "" || got.WorkspaceID == "" {
		t.Fatalf("leg workspace = %+v", got)
	}
}

func TestBindLegWorkspacesPreservesRecoveryBindings(t *testing.T) {
	binder := &stubWorkspaceBinder{}
	store := delegation.NewMemoryStore()
	ctx := context.Background()
	legs := []api.Leg{
		{ID: "running", Status: api.LegStatusRunning, WorkspaceID: "existing", WorkspaceRoot: "/existing"},
		{ID: "complete", Status: api.LegStatusComplete},
		{ID: "bound", Status: api.LegStatusPending, WorkspaceID: "prepared", WorkspaceRoot: "/prepared"},
	}
	created, err := store.Create(ctx, api.Delegation{ProjectID: "project", WorkspacePath: t.TempDir(), Task: "task", Status: "active"}, "session", legs)
	testutil.FailErr(t, "create delegation", err)
	orch := orchestration.NewOrchestratorImpl(orchestration.OrchestratorDeps{Store: store, Workspaces: binder})
	bindings, err := orch.BindLegWorkspacesIfIsolatedForTest(ctx, orchestration.TopologySpec{WorkspaceMode: orchestration.WorkspaceIsolated}, created.WorkspacePath, created.ID, []string{"running", "complete", "bound"})
	testutil.FailErr(t, "restore workspace bindings", err)
	if len(bindings) != 0 || len(binder.created) != 0 {
		t.Fatal("recovery recreated or took ownership of existing workspaces")
	}
	got, err := store.GetLeg(ctx, created.ID, "running")
	testutil.FailErr(t, "read running leg", err)
	if got.WorkspaceID != "existing" || got.WorkspaceRoot != "/existing" {
		t.Fatalf("workspace changed: %+v", got)
	}
}

func TestBindLegWorkspacesDestroysBindingsWhenLegLookupFails(t *testing.T) {
	binder := &stubWorkspaceBinder{}
	store := delegation.NewMemoryStore()
	ctx := context.Background()
	leg := api.Leg{ID: "leg-1", Title: "probe-0", Status: api.LegStatusPending}
	created, err := store.Create(ctx, api.Delegation{ProjectID: "proj-1", WorkspacePath: t.TempDir(), Task: "t", Status: "active"}, "sess", []api.Leg{leg})
	testutil.FailErr(t, "create delegation", err)
	orch := orchestration.NewOrchestratorImpl(orchestration.OrchestratorDeps{Store: store, Workspaces: binder})
	if _, err := orch.BindLegWorkspacesIfIsolatedForTest(ctx, orchestration.TopologySpec{WorkspaceMode: orchestration.WorkspaceIsolated}, created.WorkspacePath, created.ID, []string{"leg-1", "leg-ghost"}); err == nil {
		t.Fatal("expected the unknown leg to fail the bind")
	}
	if len(binder.destroyed) != 1 || binder.destroyed[0].ID != "leg-1" {
		t.Fatalf("destroyed = %v want the bound leg-1 binding", binder.destroyed)
	}
}

func TestBindLegWorkspacesDestroysBindingsWhenCreateFails(t *testing.T) {
	binder := &stubWorkspaceBinder{failCreateOn: 2}
	store := delegation.NewMemoryStore()
	ctx := context.Background()
	legs := []api.Leg{
		{ID: "leg-1", Title: "probe-0", Status: api.LegStatusPending},
		{ID: "leg-2", Title: "probe-1", Status: api.LegStatusPending},
	}
	created, err := store.Create(ctx, api.Delegation{ProjectID: "proj-1", WorkspacePath: t.TempDir(), Task: "t", Status: "active"}, "sess", legs)
	testutil.FailErr(t, "create delegation", err)
	orch := orchestration.NewOrchestratorImpl(orchestration.OrchestratorDeps{Store: store, Workspaces: binder})
	if _, err := orch.BindLegWorkspacesIfIsolatedForTest(ctx, orchestration.TopologySpec{WorkspaceMode: orchestration.WorkspaceIsolated}, created.WorkspacePath, created.ID, []string{"leg-1", "leg-2"}); err == nil {
		t.Fatal("expected the forced create failure to fail the bind")
	}
	if len(binder.destroyed) != 1 || binder.destroyed[0].ID != "leg-1" {
		t.Fatalf("destroyed = %v want the bound leg-1 binding", binder.destroyed)
	}
}

func TestBindLegWorkspacesDestroysBindingsWhenLegUpdateFails(t *testing.T) {
	binder := &stubWorkspaceBinder{}
	store := &failingUpdateStore{MemoryStore: delegation.NewMemoryStore()}
	ctx := context.Background()
	leg := api.Leg{ID: "leg-1", Title: "probe-0", Status: api.LegStatusPending}
	created, err := store.Create(ctx, api.Delegation{ProjectID: "proj-1", WorkspacePath: t.TempDir(), Task: "t", Status: "active"}, "sess", []api.Leg{leg})
	testutil.FailErr(t, "create delegation", err)
	orch := orchestration.NewOrchestratorImpl(orchestration.OrchestratorDeps{Store: store, Workspaces: binder})
	if _, err := orch.BindLegWorkspacesIfIsolatedForTest(ctx, orchestration.TopologySpec{WorkspaceMode: orchestration.WorkspaceIsolated}, created.WorkspacePath, created.ID, []string{"leg-1"}); err == nil {
		t.Fatal("expected the forced update failure to fail the bind")
	}
	if len(binder.destroyed) != 1 || binder.destroyed[0].ID != "leg-1" {
		t.Fatalf("destroyed = %v want the created leg-1 binding", binder.destroyed)
	}
}
