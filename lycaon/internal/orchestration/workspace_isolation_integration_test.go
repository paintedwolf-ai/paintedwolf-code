//go:build integration

package orchestration_test

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/delegation"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/internal/workspace"
	"github.com/lycaon/lycaon/pkg/api"
)

type dirWorkspaceBinder struct {
	mu sync.Mutex
}

func (b *dirWorkspaceBinder) CreateWorkerWorkspace(_ context.Context, primaryDir, legID string) (*workspace.Binding, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	root := filepath.Join(primaryDir, settingsoverlay.DirName(), "mock-wt", legID)
	if err := os.MkdirAll(root, 0o750); err != nil {
		return nil, err
	}
	return &workspace.Binding{ID: legID, Root: root}, nil
}

func (b *dirWorkspaceBinder) DestroyWorkerWorkspace(binding *workspace.Binding) error {
	if binding == nil {
		return nil
	}
	return nil
}

func TestPackIsolatedLegsNoCrossWrite(t *testing.T) {
	ctx := context.Background()
	binder := &dirWorkspaceBinder{}
	delStore := delegation.NewMemoryStore()
	sessStore := store.NewMemory()
	sessMgr := session.NewHost(sessStore, session.Models{Client: llm.NewMockProvider(nil), Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	queue := worker.NewInMemoryQueue(10)
	delMgr := delegation.NewManager(delStore, queue, sessMgr, delegation.AllowGate{})
	rec := &recordingDelegation{inner: delMgr, store: delStore, order: make([]string, 0, 4)}
	reg := orchestration.NewMemoryAgentRegistryForTest()
	orch := orchestration.NewOrchestratorImpl(orchestration.OrchestratorDeps{
		Delegation: rec,
		Store:      delStore,
		Agents:     reg,
		Workspaces: binder,
	})

	sess, err := sessMgr.Chats.CreateForProject(ctx, testdbseed.DefaultProjectID, api.SessionPostureOrchestrate)
	testutil.FailErr(t, "sessMgr.Create failed", err)
	projectDir := testdbseed.OrchestrationWorkspace(t, sessStore, sess)

	_, err = orch.Run(ctx, orchestration.RunRequest{
		SessionID: sess.ID,
		Topology: orchestration.TopologySpec{
			Pattern:       orchestration.TopologyPack,
			WorkspaceMode: orchestration.WorkspaceIsolated,
			Task:          "probe",
			Pack: &orchestration.PackSpec{
				ProfileID:     orchestration.ProfilePathExplorer,
				Count:         2,
				MergeStrategy: orchestration.MergeFirstValid,
			},
		},
		Input: map[string]any{"project_dir": projectDir, "project_id": testdbseed.DefaultProjectID},
	})
	testutil.FailErr(t, "orch.Run failed", err)

	delegationID, ok := delStore.DelegationBySessionID(sess.ID)
	if !ok {
		t.Fatal("missing delegation")
	}
	legs, err := delStore.ListLegs(ctx, delegationID)
	testutil.FailErr(t, "store.ListLegs failed", err)
	isolated := 0
	for _, leg := range legs {
		root := leg.WorkspaceRoot
		if root == "" {
			continue
		}
		isolated++
		marker := filepath.Join(root, leg.ID+".txt")
		if err := os.WriteFile(marker, []byte("ok"), 0o644); err != nil {
			testutil.FailErr(t, "write file", err)
		}
		if _, err := os.Stat(filepath.Join(projectDir, leg.ID+".txt")); !os.IsNotExist(err) {
			t.Fatalf("primary tree polluted by leg %q", leg.ID)
		}
	}
	if isolated != 2 {
		t.Fatalf("isolated legs = %d want 2", isolated)
	}
}
