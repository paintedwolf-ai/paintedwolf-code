package diagnostics

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/observability"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestBundleUsesRuntimeExactEvidence(t *testing.T) {
	const value = "shapeless-file-canary-3399"
	matcher, err := secretmatch.BuildMatcher(secretmatch.Bundled())
	testutil.FailErr(t, "build matcher", err)
	matcher.SetHarvestSource(func(context.Context) []secretmatch.HarvestedValue {
		return []secretmatch.HarvestedValue{{Secret: value, RuleID: secretmatch.HarvestRuleID}}
	})
	release := observability.SetCaptureRedactor(func(s string) string { return matcher.RedactString(context.Background(), s) })
	t.Cleanup(release)
	dir := t.TempDir()
	testutil.FailErr(t, "write raw log fixture", os.WriteFile(filepath.Join(dir, "sidecar.log"), []byte("upstream: "+value), 0600))
	redactor, err := NewRedactor()
	testutil.FailErr(t, "build redactor", err)
	raw, err := Build(t.Context(), Input{LogDir: dir, GeneratedAt: fixedTime(), Redactor: redactor})
	testutil.FailErr(t, "build bundle", err)
	archive, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	testutil.FailErr(t, "open zip", err)
	found := false
	for _, entry := range archive.File {
		file, err := entry.Open()
		testutil.FailErr(t, "open entry", err)
		data, err := io.ReadAll(file)
		testutil.FailErr(t, "read entry", err)
		testutil.FailErr(t, "close entry", file.Close())
		if strings.Contains(string(data), value) {
			t.Fatalf("%s retained raw evidence", entry.Name)
		}
		if strings.HasPrefix(entry.Name, "logs/") {
			found = true
		}
	}
	if !found {
		t.Fatal("bundle had no log entry")
	}
}

func TestBundleMatcherFailureWithholdsText(t *testing.T) {
	release := observability.SetCaptureRedactor(func(string) string { panic("private") })
	t.Cleanup(release)
	if got := (*Redactor)(nil).RedactText(t.Context(), "private"); got != "[REDACTED]" {
		t.Fatalf("text = %q", got)
	}
}

func TestBundleExcludesSecretAndLegacyFiles(t *testing.T) {
	entries, _ := buildTestBundle(t)
	configDir := t.TempDir()
	for _, rel := range []string{"credential-vault.age", "credential-vault-identity.age", "api.token", "host-identity.pem", "mcp.yaml", "settings.yaml"} {
		testutil.FailErr(t, "write "+rel, os.WriteFile(filepath.Join(configDir, rel), []byte("fixture-secret-value\n"), 0o600))
	}
	redactor, err := NewRedactor()
	testutil.FailErr(t, "build redactor", err)
	raw, err := Build(t.Context(), Input{
		ConfigDir:   configDir,
		GeneratedAt: fixedTime(),
		Redactor:    redactor,
	})
	testutil.FailErr(t, "build bundle", err)
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	testutil.FailErr(t, "open zip", err)
	got := make(map[string]bool, len(zr.File))
	for _, f := range zr.File {
		got[f.Name] = true
	}
	for _, forbidden := range []string{"config/credential-vault.age", "config/credential-vault-identity.age", "config/api.token", "config/host-identity.pem", "config/mcp.yaml"} {
		if got[forbidden] || entries[forbidden] != "" {
			t.Fatalf("bundle must not include excluded secret/legacy file %s", forbidden)
		}
	}
	if !got["config/settings.yaml"] {
		t.Fatal("bundle missing config/settings.yaml")
	}
}
