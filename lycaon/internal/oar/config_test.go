package oar

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestConfiguredRuleSetDisablesAndDowngrades(t *testing.T) {
	rs := NewRuleSet([]*Rule{
		{ID: "WARN_ME", Kind: KindPolicy, Anchor: AnchorToolPreInvoke, Effect: EffectWarn, Enforcement: "enforce"},
		{ID: "BLOCK_ME", Kind: KindPolicy, Anchor: AnchorToolPreInvoke, Effect: EffectBlock, Enforcement: "enforce"},
	})
	configured, err := ConfiguredRuleSet([]any{map[string]any{
		"oar_config": "1.0",
		"disable":    []any{"WARN_ME"},
		"enforcement": map[string]any{
			"BLOCK_ME": "monitor",
		},
	}}, rs)
	testutil.FailErr(t, "apply", err)
	warn, _ := configured.Get("WARN_ME")
	if !warn.Disabled || warn.Enforcement != "enforce" {
		t.Fatalf("WARN_ME disabled=%v enforcement=%q, want disabled", warn.Disabled, warn.Enforcement)
	}
	block, _ := configured.Get("BLOCK_ME")
	if block.Enforcement != "monitor" {
		t.Fatalf("BLOCK_ME enforcement = %q, want monitor", block.Enforcement)
	}
}

func TestConfiguredRuleSetRejectsMandatoryDowngrade(t *testing.T) {
	rs := NewRuleSet([]*Rule{
		{ID: "MUST", Kind: KindInvariant, Anchor: AnchorToolPreInvoke, Effect: EffectBlock, Enforcement: "enforce", Mandatory: true},
	})
	_, err := ConfiguredRuleSet([]any{map[string]any{
		"oar_config":  "1.0",
		"enforcement": map[string]any{"MUST": "monitor"},
	}}, rs)
	if err == nil {
		t.Fatal("expected mandatory downgrade to fail")
	}
}

func TestConfiguredRuleSetDoesNotMutateSharedCatalog(t *testing.T) {
	shared := NewRuleSet([]*Rule{
		{ID: "RULE", Kind: KindPolicy, Anchor: AnchorToolPreInvoke, Effect: EffectWarn, Enforcement: "enforce"},
	})
	configured, err := ConfiguredRuleSet([]any{map[string]any{
		"oar_config": "1.0",
		"disable":    []any{"RULE"},
	}}, shared)
	testutil.FailErr(t, "configure immutable rule set", err)
	sharedRule, _ := shared.Get("RULE")
	configuredRule, _ := configured.Get("RULE")
	if sharedRule.Disabled || !configuredRule.Disabled {
		t.Fatalf("shared/configured disabled = %v/%v, want false/true", sharedRule.Disabled, configuredRule.Disabled)
	}
}

func TestConfiguredProjectRuleSetMissingReturnsSharedCatalog(t *testing.T) {
	rs := NewRuleSet([]*Rule{
		{ID: "KEEP", Kind: KindPolicy, Anchor: AnchorToolPreInvoke, Effect: EffectWarn, Enforcement: "enforce"},
	})
	configured, err := ConfiguredProjectRuleSet(rs, t.TempDir())
	testutil.FailErr(t, "missing overlay", err)
	if configured != rs {
		t.Fatal("missing overlay cloned the shared catalog")
	}
	keep, _ := configured.Get("KEEP")
	if keep.Enforcement != "enforce" {
		t.Fatalf("enforcement = %q, want enforce", keep.Enforcement)
	}
}

func TestConfiguredProjectRuleSetReadsOverlayWithoutMutation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".paintedwolf")
	testutil.FailErr(t, "mkdir", os.MkdirAll(path, 0o700))
	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(path, "oar-config.yaml"), []byte("oar_config: \"1.0\"\ndisable: [KEEP]\n"), 0o600))
	rs := NewRuleSet([]*Rule{
		{ID: "KEEP", Kind: KindPolicy, Anchor: AnchorToolPreInvoke, Effect: EffectWarn, Enforcement: "enforce"},
	})
	configured, err := ConfiguredProjectRuleSet(rs, dir)
	testutil.FailErr(t, "apply overlay", err)
	keep, _ := configured.Get("KEEP")
	if !keep.Disabled {
		t.Fatalf("disabled = %v, want true", keep.Disabled)
	}
	shared, _ := rs.Get("KEEP")
	if shared.Disabled {
		t.Fatalf("shared rule was disabled")
	}
}
