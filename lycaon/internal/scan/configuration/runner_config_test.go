package configuration

import (
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

const testAgentBudget = "  agent_budget:\n    max_hints_per_injection: 8\n"

func TestDecodeRunnerConfig(t *testing.T) {
	cfg, err := decodeRunnerConfig([]byte(`
runner:
  max_concurrency: 3
  reconcile_interval_ms: 250
  retry_delay_ms: 1000
  process_priority: below_normal
  result_spill_bytes: 4194304
`))
	testutil.FailErr(t, "decode runner config", err)
	if cfg.Runner.MaxConcurrency != 3 || cfg.Runner.ReconcileIntervalMs != 250 {
		t.Fatalf("cfg = %#v", cfg)
	}
	if cfg.Runner.ProcessPriority != "below_normal" {
		t.Fatalf("process_priority = %q", cfg.Runner.ProcessPriority)
	}
	if cfg.Runner.ResultSpillBytes != 4194304 {
		t.Fatalf("result_spill_bytes = %d", cfg.Runner.ResultSpillBytes)
	}
}

func TestDefaultRunnerConfig(t *testing.T) {
	cfg := DefaultRunnerConfig()
	if cfg.Runner.ProcessPriority != "below_normal" || cfg.Runner.ResultSpillBytes != 4<<20 {
		t.Fatalf("defaults = %#v", cfg.Runner)
	}
}

func TestDecodeGatesConfig(t *testing.T) {
	cfg, err := decodeGatesConfig([]byte(`
gates:
  proactive_categories: [sca]
  landed_change:
    enabled: false
    scope: full_root
` + testAgentBudget))
	testutil.FailErr(t, "decode gates config", err)
	if len(cfg.Gates.ProactiveCategories) != 1 || cfg.Gates.ProactiveCategories[0] != api.ScanCategorySCA {
		t.Fatalf("categories = %#v", cfg.Gates.ProactiveCategories)
	}
	if cfg.Gates.LandedChange.Enabled {
		t.Fatal("expected landed_change.enabled false")
	}
	if cfg.Gates.LandedChange.Scope != LandedChangeScopeFullRoot {
		t.Fatalf("scope = %q", cfg.Gates.LandedChange.Scope)
	}
}

func TestDefaultGatesConfigLandedChangePathScoped(t *testing.T) {
	cfg := DefaultGatesConfig()
	landed := cfg.Gates.LandedChange
	if !landed.Enabled {
		t.Fatal("expected landed_change.enabled true")
	}
	if landed.Scope != LandedChangeScopePathScoped {
		t.Fatalf("scope = %q want path_scoped", landed.Scope)
	}
	cadence := cfg.Gates.Cadence
	if cadence.RefreshSettleMs != 45000 || cadence.WriteBurstSettleMs != 4000 {
		t.Fatalf("cadence settle = %#v", cadence)
	}
	if cadence.MaxDeferMs != 900000 {
		t.Fatalf("cadence max defer = %#v", cadence)
	}
	if cadence.DriftMinPaths != 8 || cadence.DriftRatio != 0.25 || cadence.ReconcileIntervalMs != 60000 {
		t.Fatalf("cadence drift/poll = %#v", cadence)
	}
}

func TestDecodeGatesConfigAcceptsPathScopedAndFullRoot(t *testing.T) {
	for _, scope := range []string{"path_scoped", "full_root"} {
		body := "gates:\n  proactive_categories: [security]\n  landed_change:\n    enabled: true\n    scope: " + scope + "\n" + testAgentBudget
		cfg, err := decodeGatesConfig([]byte(body))
		testutil.FailErr(t, "decode gates config "+scope, err)
		if string(cfg.Gates.LandedChange.Scope) != scope {
			t.Fatalf("scope = %q want %q", cfg.Gates.LandedChange.Scope, scope)
		}
	}
}

func TestDecodeGatesConfigRejectsUnknownScope(t *testing.T) {
	_, err := decodeGatesConfig([]byte(`
gates:
  proactive_categories: [security]
  landed_change:
    enabled: true
    scope: delta_only
` + testAgentBudget))
	if err == nil {
		t.Fatal("expected unknown scope to fail load")
	}
}
