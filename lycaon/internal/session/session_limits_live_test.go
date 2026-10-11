package session

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/config/configtest"
	"github.com/lycaon/lycaon/internal/llm"
	sessionstore "github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestEffectiveLimitsAppliesLiveDerived(t *testing.T) {
	tmp := t.TempDir()
	bundledPolicy := filepath.Join(tmp, "policy.yaml")
	globalPolicy := filepath.Join(tmp, "global-policy.yaml")
	if err := os.WriteFile(bundledPolicy, []byte(`
coordinator:
  provider_id: fw
  model: accounts/fireworks/models/kimi-k2p7-code
lite:
  provider_id: ""
  model: ""
agent_pool:
  selection: first
  models: []
`), 0o600); err != nil {
		t.Fatal(err)
	}
	policy, err := llm.NewPolicyStoreAt(globalPolicy)
	testutil.FailErr(t, "NewPolicyStoreAt", err)

	configtest.Overlay(t, map[config.Rel]string{
		config.SessionLimits: "max_iterations: 10\nmax_tool_spill_bytes: 1024\n",
	})
	globalLimits := filepath.Join(tmp, "global-limits.yaml")
	limStore, err := settings.NewLimitsStoreAt(globalLimits)
	testutil.FailErr(t, "NewLimitsStoreAt", err)

	mgr := NewHost(sessionstore.NewMemory(), Models{Client: nil, Provider: &llm.Service{Policy: policy}, Limits: settings.SessionLimits{}, Cost: nil}, nil)
	mgr.Limits.SetProvider(settings.ProjectLimitsAdapter{Store: limStore})

	sess := &api.Session{WorkspacePath: tmp}
	got := mgr.Limits.Effective(t.Context(), sess)
	if got.MaxToolResultBytes != 524288 || got.MaxCoordinatorLoopCycles != 256 {
		t.Fatalf("kimi derived limits = bytes %d cycles %d", got.MaxToolResultBytes, got.MaxCoordinatorLoopCycles)
	}

	testutil.FailErr(t, "PutGlobal overlay", limStore.PutGlobal(settings.SessionLimits{MaxToolResultBytes: 99999}))
	got = mgr.Limits.Effective(t.Context(), sess)
	if got.MaxToolResultBytes != 99999 {
		t.Fatalf("overlay MaxToolResultBytes = %d want 99999", got.MaxToolResultBytes)
	}
	if got.MaxCoordinatorLoopCycles != 256 {
		t.Fatalf("cycles = %d want derived 64", got.MaxCoordinatorLoopCycles)
	}
}
