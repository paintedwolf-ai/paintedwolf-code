package session

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/config/configtest"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestCompactionConfigFuncReapplies(t *testing.T) {
	tmp := t.TempDir()
	global := filepath.Join(tmp, "global.yaml")
	if err := os.WriteFile(global, []byte(`
coordinator:
  provider_id: openai
  model: gpt-4o
lite:
  provider_id: ""
  model: ""
agent_pool:
  selection: first
  models: []
`), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := llm.NewPolicyStoreAt(global)
	testutil.FailErr(t, "NewPolicyStoreAt", err)

	mgr := &Manager{llmSvc: &llm.Service{Policy: store}}
	first, _ := mgr.liveBudgetResolveForRoots(nil)
	if first.ModelContextWindow != 96000 { // 128000 * 75%
		t.Fatalf("gpt-4o live = %d want 96000", first.ModelContextWindow)
	}

	testutil.FailErr(t, "PutGlobal kimi", store.PutGlobal(llm.ModelPolicy{
		Coordinator: llm.ModelRef{ProviderID: "fw", Model: "accounts/fireworks/models/kimi-k2p7-code"},
	}))
	second, _ := mgr.liveBudgetResolveForRoots(nil)
	if second.ModelContextWindow != 196608 {
		t.Fatalf("kimi live = %d want 196608 (policy change without restart)", second.ModelContextWindow)
	}
	if first.ModelContextWindow == second.ModelContextWindow {
		t.Fatal("expected live budget to change after policy model change")
	}
}

func TestLiveBudgetResolveUsesProjectOverlay(t *testing.T) {
	tmp := t.TempDir()
	global := filepath.Join(tmp, "global.yaml")
	projectDir := filepath.Join(tmp, "proj")
	if err := os.MkdirAll(filepath.Join(projectDir, settingsoverlay.DirName()), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	configtest.Overlay(t, map[config.Rel]string{config.ModelPolicy: `
coordinator:
  provider_id: openai
  model: gpt-4o
lite:
  provider_id: ""
  model: ""
agent_pool:
  selection: first
  models: []
`})
	store, err := llm.NewPolicyStoreAt(global)
	testutil.FailErr(t, "NewPolicyStoreAt", err)
	testutil.FailErr(t, "PutGlobal", store.PutGlobal(llm.ModelPolicy{
		Coordinator: llm.ModelRef{ProviderID: "openai", Model: "gpt-4o"},
	}))
	testutil.FailErr(t, "PutProject", store.PutProject(projectDir, llm.ModelPolicy{
		Coordinator: llm.ModelRef{ProviderID: "fw", Model: "accounts/fireworks/models/kimi-k2p7-code"},
	}))

	mgr := &Manager{llmSvc: &llm.Service{Policy: store}}
	globalCfg, _ := mgr.liveBudgetResolveForRoots(nil)
	if globalCfg.ModelContextWindow != 96000 {
		t.Fatalf("global live = %d want 96000", globalCfg.ModelContextWindow)
	}
	ignored, _ := mgr.liveBudgetResolveForRoots(nil)
	if ignored.ModelContextWindow != 96000 {
		t.Fatalf("empty dir must stay global: %d", ignored.ModelContextWindow)
	}
	projectCfg, _ := mgr.liveBudgetResolveForRoots([]string{projectDir})
	if projectCfg.ModelContextWindow != 196608 {
		t.Fatalf("project live = %d want 196608", projectCfg.ModelContextWindow)
	}
}
