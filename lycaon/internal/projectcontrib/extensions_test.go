package projectcontrib

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testutil"
)

func writeExtensionSurfaceFile(t *testing.T, root, name, body string) {
	t.Helper()
	dir := settingsoverlay.Dir(root)
	testutil.FailErr(t, "create overlay", os.MkdirAll(dir, 0o700))
	testutil.FailErr(t, "write extension surface", os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600))
}

func TestExtensionCaptureCoversSuggestions(t *testing.T) {
	root := t.TempDir()
	writeExtensionSurfaceFile(t, root, settingsoverlay.BasenameExtensions, `
format: 1
suggest:
  - id: acme/triage
    source: https://example.com/acme/triage.git
    version: ^1.0.0
`)
	baseline := scanExtensionSuggestions([]string{root})
	if baseline.Count != 1 || baseline.Items[0].Name != "acme/triage" {
		t.Fatalf("suggestion items = %+v", baseline.Items)
	}

	writeExtensionSurfaceFile(t, root, settingsoverlay.BasenameExtensions, `
format: 1
suggest:
  - id: acme/triage
    source: https://example.com/acme/triage.git
    version: ^2.0.0
`)
	versionChanged := scanExtensionSuggestions([]string{root}).Files
	if slices.Equal(versionChanged, baseline.Files) {
		t.Fatal("suggestion capture omitted the version change")
	}

	writeExtensionSurfaceFile(t, root, settingsoverlay.BasenameExtensionsLock, `
lock_format: 1
packages:
  - id: acme/triage
    version: 2.1.0
    source: https://example.com/acme/triage.git
    revision: abc123
    integrity: sha256:first
    kind: git
`)
	withLock := scanExtensionSuggestions([]string{root})
	if !slices.Equal(withLock.Files, versionChanged) {
		t.Fatal("project lock changed the captured suggestion files")
	}
}

func TestExtensionSuggestionsIgnorePackRows(t *testing.T) {
	root := t.TempDir()
	writeExtensionSurfaceFile(t, root, settingsoverlay.BasenameExtensions, `
format: 1
packs:
  - id: acme/device
    source: https://example.com/acme/device.git
    version: ^1.0.0
`)
	got := scanExtensionSuggestions([]string{root})
	if got.Count != 0 {
		t.Fatalf("device pack rows reported as suggestions: %+v", got.Items)
	}
}

func TestExtensionSettingsAndSuggestionsScanSeparately(t *testing.T) {
	root := t.TempDir()
	writeExtensionSurfaceFile(t, root, settingsoverlay.BasenameExtensions, `
format: 1
disabled:
  - workflows/bugbash
`)
	suggestions := scanExtensionSuggestions([]string{root})
	if suggestions.Count != 0 || len(suggestions.Items) != 0 {
		t.Fatalf("unit disables reported as install suggestions: %+v", suggestions)
	}
	config := scanExtensionConfig([]string{root})
	if config.Count != 1 || config.Items[0].Name != "workflows/bugbash" {
		t.Fatalf("extension settings = %+v, want unit disable", config.Items)
	}
}
