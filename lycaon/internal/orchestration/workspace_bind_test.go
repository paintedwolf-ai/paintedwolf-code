package orchestration_test

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/delegation"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workspace"
	"github.com/lycaon/lycaon/pkg/api"
)

type stubWorkspaceBinder struct {
	created []*workspace.Binding
}

func (s *stubWorkspaceBinder) CreateWorkerWorkspace(_ context.Context, _, legID string) (*workspace.Binding, error) {
	b := &workspace.Binding{ID: legID, Root: "/tmp/" + legID}
	s.created = append(s.created, b)
	return b, nil
}

func (s *stubWorkspaceBinder) DestroyWorkerWorkspace(*workspace.Binding) error {
	return nil
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
