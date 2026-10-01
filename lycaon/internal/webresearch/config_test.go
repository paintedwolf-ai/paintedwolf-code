package webresearch

import (
	"path/filepath"
	"testing"
)

func TestConfigStoreWarmingBoolDefaults(t *testing.T) {
	t.Parallel()
	store := NewConfigStoreAt(filepath.Join(t.TempDir(), "missing.yaml"))
	if !store.WarmingEnabled() || !store.GuessDomains() {
		t.Fatalf("unset defaults: warming=%v guess=%v want true/true", store.WarmingEnabled(), store.GuessDomains())
	}
	if store.Warming() != WarmingFull {
		t.Fatalf("Warming() = %q want full", store.Warming())
	}
}
