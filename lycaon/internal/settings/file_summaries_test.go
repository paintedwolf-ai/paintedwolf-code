package settings_test

import (
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestFileSummariesStoreDefaultsOnAndPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file-summaries.yaml")
	store, err := settings.NewFileSummariesStoreAt(path)
	testutil.FailErr(t, "load default File summaries setting", err)
	if !store.Enabled() {
		t.Fatal("File summaries should default enabled")
	}

	testutil.FailErr(t, "disable File summaries", store.PutEnabled(false))
	loaded, err := settings.NewFileSummariesStoreAt(path)
	testutil.FailErr(t, "reload File summaries setting", err)
	if loaded.Enabled() {
		t.Fatal("disabled File summaries setting was not persisted")
	}
}
