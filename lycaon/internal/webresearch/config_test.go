package webresearch

import (
	"bytes"
	"github.com/lycaon/lycaon/internal/testutil"
	"os"
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

func TestOpenConfigStorePreservesUnreadablePreferences(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "prefs.yaml")
	original := []byte("providers: [broken\n")
	testutil.FailErr(t, "write malformed preferences", os.WriteFile(path, original, 0o600))
	store, err := OpenConfigStore(path)
	if err == nil || store != nil {
		t.Fatal("malformed preferences opened")
	}
	preserved, err := os.ReadFile(path)
	testutil.FailErr(t, "read preserved preferences", err)
	if !bytes.Equal(preserved, original) {
		t.Fatal("opening changed malformed preferences")
	}
	testutil.FailErr(t, "repair preferences", os.WriteFile(path, []byte("warming: false\n"), 0o600))
	recovered, err := OpenConfigStore(path)
	testutil.FailErr(t, "open repaired preferences", err)
	if recovered.WarmingEnabled() {
		t.Fatal("repaired preference was not loaded")
	}
}

func TestOpenConfigStoreRejectsInvalidResources(t *testing.T) {
	t.Parallel()
	for _, path := range []string{"", t.TempDir()} {
		if store, err := OpenConfigStore(path); err == nil || store != nil {
			t.Fatalf("opened invalid config resource %q", path)
		}
	}
}

func TestOpenConfigStoreDoesNotCreateAbsentPreferences(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "prefs.yaml")
	_, err := OpenConfigStore(path)
	testutil.FailErr(t, "open defaults", err)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("opening defaults created file: %v", err)
	}
}
