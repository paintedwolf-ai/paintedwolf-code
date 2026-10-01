package webresearch

import (
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestApplyPrefsLeavesLiveStateUntouchedWhenSaveFails(t *testing.T) {
	store := NewConfigStoreAt(filepath.Join(t.TempDir(), "prefs.yaml"))
	initialWarming := false
	testutil.FailErr(t, "save initial prefs", store.ApplyPrefs(&initialWarming, nil, nil, []string{"direct"}))
	store.path = t.TempDir()

	nextWarming := true
	if err := store.ApplyPrefs(&nextWarming, nil, nil, []string{"brave"}); err == nil {
		t.Fatal("ApplyPrefs succeeded with a directory as its destination")
	}
	if store.WarmingEnabled() {
		t.Fatal("failed save changed live warming preference")
	}
	if got, _ := store.ProviderSelection(); len(got) != 1 || got[0] != "direct" {
		t.Fatalf("failed save changed live providers: %v", got)
	}
}

func TestSetProviderConfigLeavesLiveStateUntouchedWhenSaveFails(t *testing.T) {
	store := NewConfigStoreAt(filepath.Join(t.TempDir(), "prefs.yaml"))
	testutil.FailErr(t, "save initial config", store.SetProviderConfig("searxng", map[string]string{
		"endpoint": "https://one.example",
	}))
	store.path = t.TempDir()

	if err := store.SetProviderConfig("searxng", map[string]string{"endpoint": "https://two.example"}); err == nil {
		t.Fatal("SetProviderConfig succeeded with a directory as its destination")
	}
	if got := store.ProviderConfig("searxng")["endpoint"]; got != "https://one.example" {
		t.Fatalf("failed save changed live endpoint to %q", got)
	}
}

func TestWarmingCapsLoadsScheduledProbeBudget(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prefs.yaml")
	store := NewConfigStoreAt(path)
	raw := cloneConfigFile(store.raw)
	raw.WarmingCaps.ScheduledProbes = 7
	testutil.FailErr(t, "save warming caps", store.save(raw))

	loaded := NewConfigStoreAt(path)
	if got := loaded.WarmingCaps().ScheduledProbes; got != 7 {
		t.Fatalf("scheduled probes = %d want 7", got)
	}
}
