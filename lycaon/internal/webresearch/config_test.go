package webresearch

import (
	"github.com/lycaon/lycaon/internal/testutil"
	"path/filepath"
	"testing"
)

func TestConfigStoreWarmingBoolDefaults(t *testing.T) {
	t.Parallel()
	store, openErr := OpenConfigStore(filepath.Join(t.TempDir(), "missing.yaml"))
	testutil.FailErr(t, "open web research store", openErr)
	if !store.WarmingEnabled() || !store.GuessDomains() {
		t.Fatalf("unset defaults: warming=%v guess=%v want true/true", store.WarmingEnabled(), store.GuessDomains())
	}
	if store.Warming() != WarmingFull {
		t.Fatalf("Warming() = %q want full", store.Warming())
	}
}
