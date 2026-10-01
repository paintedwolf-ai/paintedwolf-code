package contract

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/hintregistry"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestBundledHintRegistryIsStockPackUnion(t *testing.T) {
	t.Parallel()
	module := filepath.Join(contractcheck.RepoRoot(t), "lycaon")
	entries, err := hintregistry.ListEffective()
	contractcheck.FailErr(t, "ListStock", err)
	if len(entries) < 300 {
		t.Fatalf("stock policy union too small: %d entries", len(entries))
	}
	// Platform remains one pack among many — not the sole authoring root.
	platform := filepath.Join(module, "config", "packs", "painted-wolf", "platform", "policy")
	info, err := os.Stat(platform)
	contractcheck.FailErr(t, "stat platform policy dir", err)
	if !info.IsDir() {
		t.Fatalf("%s must be a directory", platform)
	}
	mono := filepath.Join(module, "config", "packs", "painted-wolf", "platform", "policy.yaml")
	if _, err := os.Stat(mono); err == nil {
		t.Fatal("monolithic policy.yaml must not exist")
	}
	hitl := filepath.Join(module, "config", "packs", "painted-wolf", "hitl", "policy")
	if _, err := os.Stat(hitl); err != nil {
		t.Fatalf("hitl policy pack required for stock union: %v", err)
	}
}
