package settings

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestWriteSettingsFileCreatesPrivateParentAndReplacesContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "settings.yaml")
	testutil.FailErr(t, "write initial settings", writeSettingsFile(path, []byte("value: one\n")))
	testutil.FailErr(t, "replace settings", writeSettingsFile(path, []byte("value: two\n")))

	data, err := os.ReadFile(path)
	testutil.FailErr(t, "read replaced settings", err)
	if got := string(data); got != "value: two\n" {
		t.Fatalf("settings content = %q, want replacement", got)
	}
	info, err := os.Stat(path)
	testutil.FailErr(t, "stat settings", err)
	if got := info.Mode().Perm(); got != settingsFileMode {
		t.Fatalf("settings mode = %o, want %o", got, settingsFileMode)
	}
	parentInfo, err := os.Stat(filepath.Dir(path))
	testutil.FailErr(t, "stat settings parent", err)
	if got := parentInfo.Mode().Perm(); got != 0o700 {
		t.Fatalf("settings parent mode = %o, want 700", got)
	}
	matches, err := filepath.Glob(path + ".*.tmp")
	testutil.FailErr(t, "glob temporary settings", err)
	if len(matches) != 0 {
		t.Fatalf("temporary settings remain: %v", matches)
	}
}

func TestReviewStorePutGlobalUsesConfiguredPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "custom", "review.yaml")
	store := &ReviewStore{
		globalPath:   path,
		projectCache: make(map[string]ReviewConfig),
	}
	testutil.FailErr(t, "put global review settings", store.PutGlobal(ReviewConfig{
		ReviewPaths: []ContentReviewRule{{Path: "docs/**"}},
	}))
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("configured review path was not written: %v", err)
	}
}
