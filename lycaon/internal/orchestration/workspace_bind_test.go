package orchestration_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

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

func (s *stubWorkspaceBinder) CreateWorkerWorkspace(_ context.Context, _, unitID string) (*workspace.Binding, error) {
	s.creates++
	if s.failCreateOn == s.creates {
		return nil, fmt.Errorf("create worker workspace forced failure")
	}
	b := &workspace.Binding{ID: unitID, Root: "/tmp/" + unitID}
	s.created = append(s.created, b)
	return b, nil
}

func (s *stubWorkspaceBinder) DestroyWorkerWorkspace(_ context.Context, b *workspace.Binding) error {
	s.destroyed = append(s.destroyed, b)
	return nil
}

// failingUpdateWorkers forces the UpdateUnit error path while every other worker
// operation behaves like the memory store.
type failingUpdateWorkers struct {
	*orchestration.MemoryTopologyWorkers
}

func (s *failingUpdateWorkers) UpdateUnit(context.Context, string, orchestration.TopologyUnit) error {
	return errors.New("update unit forced failure")
}

func TestBindLegWorkspacesSharedSkips(t *testing.T) {
	workers := orchestration.NewMemoryTopologyWorkers()
	orch := orchestration.NewOrchestratorImpl(orchestration.OrchestratorDeps{Workers: workers})
	bindings, err := orch.BindLegWorkspacesIfIsolatedForTest(context.Background(), orchestration.TopologySpec{WorkspaceMode: orchestration.WorkspaceShared}, "/tmp", "batch", []string{"unit-1"})
	if err != nil || len(bindings) != 0 {
		t.Fatalf("bindings = %v err=%v", bindings, err)
	}
}

func TestBindLegWorkspacesIsolatedUpdatesLeg(t *testing.T) {
	binder := &stubWorkspaceBinder{}
	workers := orchestration.NewMemoryTopologyWorkers()
	ctx := context.Background()
	unit := orchestration.TopologyUnit{ID: "unit-1", WorkID: "probe-0", Status: api.LegStatusPending}
	dir := t.TempDir()
	created, _, err := workers.CreateBatch(ctx, orchestration.TopologyBatch{SessionID: "sess", ProjectID: "proj-1", WorkspacePath: dir, Task: "t"}, []orchestration.TopologyUnit{unit})
	testutil.FailErr(t, "create batch", err)
	orch := orchestration.NewOrchestratorImpl(orchestration.OrchestratorDeps{Workers: workers, Workspaces: binder})
	bindings, err := orch.BindLegWorkspacesIfIsolatedForTest(ctx, orchestration.TopologySpec{
		Pattern:       orchestration.TopologyPack,
		WorkspaceMode: orchestration.WorkspaceIsolated,
	}, dir, created.ID, []string{"unit-1"})
	testutil.FailErr(t, "orch.BindLegWorkspacesIfIsolatedForTest failed", err)
	if len(bindings) != 1 {
		t.Fatalf("bindings = %d", len(bindings))
	}
	got, err := workers.GetUnit(ctx, created.ID, "unit-1")
	testutil.FailErr(t, "workers.GetUnit failed", err)
	if got.WorkspaceRoot == "" || got.WorkspaceID == "" {
		t.Fatalf("unit workspace = %+v", got)
	}
}

func TestBindLegWorkspacesPreservesRecoveryBindings(t *testing.T) {
	binder := &stubWorkspaceBinder{}
	workers := orchestration.NewMemoryTopologyWorkers()
	ctx := context.Background()
	units := []orchestration.TopologyUnit{
		{ID: "running", WorkID: "r", Status: api.LegStatusRunning, WorkspaceID: "existing", WorkspaceRoot: "/existing"},
		{ID: "complete", WorkID: "c", Status: api.LegStatusComplete},
		{ID: "bound", WorkID: "b", Status: api.LegStatusPending, WorkspaceID: "prepared", WorkspaceRoot: "/prepared"},
	}
	dir := t.TempDir()
	created, _, err := workers.CreateBatch(ctx, orchestration.TopologyBatch{SessionID: "session", ProjectID: "project", WorkspacePath: dir, Task: "task"}, units)
	testutil.FailErr(t, "create batch", err)
	orch := orchestration.NewOrchestratorImpl(orchestration.OrchestratorDeps{Workers: workers, Workspaces: binder})
	bindings, err := orch.BindLegWorkspacesIfIsolatedForTest(ctx, orchestration.TopologySpec{WorkspaceMode: orchestration.WorkspaceIsolated}, dir, created.ID, []string{"running", "complete", "bound"})
	testutil.FailErr(t, "restore workspace bindings", err)
	if len(bindings) != 0 || len(binder.created) != 0 {
		t.Fatal("recovery recreated or took ownership of existing workspaces")
	}
	got, err := workers.GetUnit(ctx, created.ID, "running")
	testutil.FailErr(t, "read running unit", err)
	if got.WorkspaceID != "existing" || got.WorkspaceRoot != "/existing" {
		t.Fatalf("workspace changed: %+v", got)
	}
}

func TestBindLegWorkspacesDestroysBindingsWhenLegLookupFails(t *testing.T) {
	binder := &stubWorkspaceBinder{}
	workers := orchestration.NewMemoryTopologyWorkers()
	ctx := context.Background()
	dir := t.TempDir()
	created, _, err := workers.CreateBatch(ctx, orchestration.TopologyBatch{SessionID: "sess", ProjectID: "proj-1", WorkspacePath: dir, Task: "t"}, []orchestration.TopologyUnit{{ID: "unit-1", WorkID: "probe-0", Status: api.LegStatusPending}})
	testutil.FailErr(t, "create batch", err)
	orch := orchestration.NewOrchestratorImpl(orchestration.OrchestratorDeps{Workers: workers, Workspaces: binder})
	if _, err := orch.BindLegWorkspacesIfIsolatedForTest(ctx, orchestration.TopologySpec{WorkspaceMode: orchestration.WorkspaceIsolated}, dir, created.ID, []string{"unit-1", "unit-ghost"}); err == nil {
		t.Fatal("expected the unknown unit to fail the bind")
	}
	if len(binder.destroyed) != 1 || binder.destroyed[0].ID != "unit-1" {
		t.Fatalf("destroyed = %v want the bound unit-1 binding", binder.destroyed)
	}
}

func TestBindLegWorkspacesDestroysBindingsWhenCreateFails(t *testing.T) {
	binder := &stubWorkspaceBinder{failCreateOn: 2}
	workers := orchestration.NewMemoryTopologyWorkers()
	ctx := context.Background()
	dir := t.TempDir()
	units := []orchestration.TopologyUnit{
		{ID: "unit-1", WorkID: "probe-0", Status: api.LegStatusPending},
		{ID: "unit-2", WorkID: "probe-1", Status: api.LegStatusPending},
	}
	created, _, err := workers.CreateBatch(ctx, orchestration.TopologyBatch{SessionID: "sess", ProjectID: "proj-1", WorkspacePath: dir, Task: "t"}, units)
	testutil.FailErr(t, "create batch", err)
	orch := orchestration.NewOrchestratorImpl(orchestration.OrchestratorDeps{Workers: workers, Workspaces: binder})
	if _, err := orch.BindLegWorkspacesIfIsolatedForTest(ctx, orchestration.TopologySpec{WorkspaceMode: orchestration.WorkspaceIsolated}, dir, created.ID, []string{"unit-1", "unit-2"}); err == nil {
		t.Fatal("expected the forced create failure to fail the bind")
	}
	if len(binder.destroyed) != 1 || binder.destroyed[0].ID != "unit-1" {
		t.Fatalf("destroyed = %v want the bound unit-1 binding", binder.destroyed)
	}
}

func TestBindLegWorkspacesDestroysBindingsWhenLegUpdateFails(t *testing.T) {
	binder := &stubWorkspaceBinder{}
	workers := &failingUpdateWorkers{MemoryTopologyWorkers: orchestration.NewMemoryTopologyWorkers()}
	ctx := context.Background()
	dir := t.TempDir()
	created, _, err := workers.CreateBatch(ctx, orchestration.TopologyBatch{SessionID: "sess", ProjectID: "proj-1", WorkspacePath: dir, Task: "t"}, []orchestration.TopologyUnit{{ID: "unit-1", WorkID: "probe-0", Status: api.LegStatusPending}})
	testutil.FailErr(t, "create batch", err)
	orch := orchestration.NewOrchestratorImpl(orchestration.OrchestratorDeps{Workers: workers, Workspaces: binder})
	if _, err := orch.BindLegWorkspacesIfIsolatedForTest(ctx, orchestration.TopologySpec{WorkspaceMode: orchestration.WorkspaceIsolated}, dir, created.ID, []string{"unit-1"}); err == nil {
		t.Fatal("expected the forced update failure to fail the bind")
	}
	if len(binder.destroyed) != 1 || binder.destroyed[0].ID != "unit-1" {
		t.Fatalf("destroyed = %v want the created unit-1 binding", binder.destroyed)
	}
}
