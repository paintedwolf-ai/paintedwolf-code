//go:build integration

package session_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
	"gopkg.in/yaml.v3"
)

func TestManagerEffectiveLimitsUsesProjectOverlay(t *testing.T) {
	projectDir := t.TempDir()
	lycaonDir := filepath.Join(projectDir, settingsoverlay.DirName())
	if err := os.MkdirAll(lycaonDir, 0o755); err != nil {
		testutil.FailErr(t, "create directory", err)
	}
	overlay := settings.SessionLimits{MaxIterations: 2}
	data, err := yaml.Marshal(overlay)
	testutil.FailErr(t, "yaml.Marshal failed", err)
	if err := os.WriteFile(filepath.Join(lycaonDir, "limits.yaml"), data, 0o600); err != nil {
		testutil.FailErr(t, "write file", err)
	}

	limitsStore, err := settings.NewLimitsStoreAt(filepath.Join(t.TempDir(), "global-limits.yaml"))
	testutil.FailErr(t, "settings.NewLimitsStoreAt failed", err)

	mgr := session.NewManager(store.NewMemory(), llm.NewMockProvider(&llm.MockConfig{
		Responses: []llm.MockResponseEntry{{Pattern: ".", Text: "ok"}},
	}), tools.NewStubRegistry(), settings.DefaultSessionLimits())
	mgr.SetLimitsProvider(settings.ProjectLimitsAdapter{Store: limitsStore})
	mgr.SetMaxIterations(99)

	// Project limits require project settings trust.
	projectID := session.RegisterProjectContextForTest(t, mgr, projectDir)
	sess := &api.Session{ProjectID: projectID, WorkspacePath: projectDir}
	limits := mgr.EffectiveLimitsForTest(t.Context(), sess)
	if limits.MaxIterations != 2 {
		t.Fatalf("effective max_iterations = %d want 2 from project overlay", limits.MaxIterations)
	}
}

func TestManagerEffectiveLimitsUsesHostWorkerMaxToolLoops(t *testing.T) {
	mgr := session.NewManager(store.NewMemory(), nil, tools.NewStubRegistry(), settings.DefaultSessionLimits())

	child := &api.Session{WorkspacePath: t.TempDir(), AgentType: orchestration.ProfilePathExplorer, ParentSessionID: "parent-1"}
	limits := mgr.EffectiveLimitsForTest(t.Context(), child)
	if limits.MaxIterations != spawn.DefaultWorkerMaxToolLoops {
		t.Fatalf("max_iterations = %d want default %d", limits.MaxIterations, spawn.DefaultWorkerMaxToolLoops)
	}

	child.MaxToolLoops = 5
	if lim := mgr.EffectiveLimitsForTest(t.Context(), child); lim.MaxIterations != 5 {
		t.Fatalf("override max_iterations = %d want 5", lim.MaxIterations)
	}

	coord := &api.Session{WorkspacePath: child.WorkspacePath, AgentType: orchestration.ProfileCoordinator}
	if lim := mgr.EffectiveLimitsForTest(t.Context(), coord); lim.MaxIterations != settings.DefaultSessionLimits().MaxIterations {
		t.Fatalf("coordinator max_iterations = %d want session default", lim.MaxIterations)
	}
}

func TestManagerEffectiveLimitsWithoutProviderUsesStaticCfg(t *testing.T) {
	cfg := settings.DefaultSessionLimits()
	cfg.MaxIterations = 7
	mgr := session.NewManager(store.NewMemory(), nil, tools.NewStubRegistry(), cfg)
	sess := &api.Session{WorkspacePath: t.TempDir()}
	limits := mgr.EffectiveLimitsForTest(t.Context(), sess)
	if limits.MaxIterations != 7 {
		t.Fatalf("max_iterations = %d want 7", limits.MaxIterations)
	}
	_ = context.Background()
}
