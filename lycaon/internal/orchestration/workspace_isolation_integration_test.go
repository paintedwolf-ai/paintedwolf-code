//go:build integration

package orchestration_test

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store/storetest"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/workspace"
	"github.com/lycaon/lycaon/pkg/api"
)

type dirWorkspaceBinder struct {
	mu sync.Mutex
}

func (b *dirWorkspaceBinder) CreateWorkerWorkspace(_ context.Context, primaryDir, unitID string) (*workspace.Binding, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	root := filepath.Join(primaryDir, settingsoverlay.DirName(), "mock-wt", unitID)
	if err := os.MkdirAll(root, 0o750); err != nil {
		return nil, err
	}
	return &workspace.Binding{ID: unitID, Root: root}, nil
}

func (b *dirWorkspaceBinder) DestroyWorkerWorkspace(_ context.Context, binding *workspace.Binding) error {
	if binding == nil {
		return nil
	}
	return nil
}

func TestPackIsolatedLegsNoCrossWrite(t *testing.T) {
	ctx := context.Background()
	binder := &dirWorkspaceBinder{}
	mem := orchestration.NewMemoryTopologyWorkers()
	sessStore := storetest.New(t, testdbseed.DefaultProjectID)
	sessMgr := session.NewHost(sessStore, session.Models{Client: llm.NewMockProvider(nil), Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	rec := &recordingWorkers{TopologyWorkers: mem, order: make([]string, 0, 4)}
	reg := orchestration.NewMemoryAgentRegistryForTest()
	orch := orchestration.NewOrchestratorImpl(orchestration.OrchestratorDeps{
		Workers:    rec,
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

	batchID, ok, err := mem.BatchBySession(ctx, sess.ID)
	testutil.FailErr(t, "BatchBySession failed", err)
	if !ok {
		t.Fatal("missing topology batch")
	}
	units, err := mem.ListUnits(ctx, batchID)
	testutil.FailErr(t, "mem.ListUnits failed", err)
	isolated := 0
	for _, u := range units {
		root := u.WorkspaceRoot
		if root == "" {
			continue
		}
		isolated++
		marker := filepath.Join(root, u.ID+".txt")
		if err := os.WriteFile(marker, []byte("ok"), 0o644); err != nil {
			testutil.FailErr(t, "write file", err)
		}
		if _, err := os.Stat(filepath.Join(projectDir, u.ID+".txt")); !os.IsNotExist(err) {
			t.Fatalf("primary tree polluted by unit %q", u.ID)
		}
	}
	if isolated != 2 {
		t.Fatalf("isolated units = %d want 2", isolated)
	}
}
