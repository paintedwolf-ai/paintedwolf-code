package diagnostics

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"

	"github.com/lycaon/lycaon/internal/localdata"
)

const (
	fakeKey   = "sk-live-0123456789abcdefghijklmnop"
	fakeToken = "ghp_0123456789abcdefghijklmnopqrstuvwx"
)

func fixedTime() time.Time { return time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC) }

// buildTestBundle lays out a config dir and log dir seeded with secrets, then
// builds the bundle and returns its entries by name.
func buildTestBundle(t *testing.T) (map[string]string, []byte) {
	t.Helper()

	configDir := t.TempDir()
	logDir := t.TempDir()

	write := func(dir, name, content string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			testutil.FailErr(t, "write fixture "+name, err)
		}
	}

	// Secret stores stay out of the bundle.
	write(configDir, "credential-vault.age", "encrypted "+fakeKey+"\n")
	write(configDir, "credential-vault-identity.age", "wrapped "+fakeToken+"\n")
	write(configDir, "api.token", fakeToken)
	// Ordinary config that may still carry an inline secret.
	write(configDir, "settings.yaml", "posture: balanced\nmcp:\n  authorization: Bearer "+fakeToken+"\n")
	// Logs that captured a key.
	write(logDir, "sidecar.log", "starting\nAuthorization: Bearer "+fakeKey+"\nready\n")

	redactor, err := NewRedactor()
	testutil.FailErr(t, "build diagnostics redactor", err)
	raw, err := Build(t.Context(), Input{
		Health:      map[string]any{"status": "ok", "version": "0.1.0", "schema_version": 12},
		Preflight:   map[string]any{"overall": "degraded", "probes": []any{map[string]any{"id": "git_engine", "status": "degraded"}}},
		ConfigDir:   configDir,
		LogDir:      logDir,
		GeneratedAt: fixedTime(),
		Redactor:    redactor,
	})
	if err != nil {
		testutil.FailErr(t, "build bundle", err)
	}

	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		testutil.FailErr(t, "open bundle zip", err)
	}

	entries := make(map[string]string, len(zr.File))
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			testutil.FailErr(t, "open entry "+f.Name, err)
		}
		var buf bytes.Buffer
		if _, err := buf.ReadFrom(rc); err != nil {
			testutil.FailErr(t, "read entry "+f.Name, err)
		}
		_ = rc.Close()
		entries[f.Name] = buf.String()
	}
	return entries, raw
}

func TestBundleContainsTheExpectedShape(t *testing.T) {
	entries, _ := buildTestBundle(t)

	for _, want := range []string{"manifest.json", "health.json", "preflight.json", "config/settings.yaml", "logs/sidecar.log"} {
		if _, ok := entries[want]; !ok {
			t.Fatalf("bundle is missing %s (entries: %v)", want, keys(entries))
		}
	}
}

func TestBundleHealthInheritsPreviousAppVersion(t *testing.T) {
	health := map[string]any{
		"status":               "ok",
		"version":              "0.1.1",
		"store_revision":       1,
		"schema_version":       1,
		"previous_app_version": "0.1.0",
	}
	raw, err := Build(t.Context(), Input{
		Health:      health,
		Preflight:   map[string]any{"overall": "ok"},
		ConfigDir:   t.TempDir(),
		LogDir:      t.TempDir(),
		GeneratedAt: fixedTime(),
	})
	testutil.FailErr(t, "build bundle", err)

	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	testutil.FailErr(t, "open zip", err)
	var healthJSON string
	for _, f := range zr.File {
		if f.Name != "health.json" {
			continue
		}
		rc, err := f.Open()
		testutil.FailErr(t, "open health.json", err)
		var buf bytes.Buffer
		_, err = buf.ReadFrom(rc)
		_ = rc.Close()
		testutil.FailErr(t, "read health.json", err)
		healthJSON = buf.String()
		break
	}
	if healthJSON == "" {
		t.Fatal("health.json missing from bundle")
	}
	var got map[string]any
	testutil.FailErr(t, "decode health.json", json.Unmarshal([]byte(healthJSON), &got))
	if got["previous_app_version"] != "0.1.0" {
		t.Fatalf("health.json previous_app_version = %#v want 0.1.0", got["previous_app_version"])
	}
}

func TestBundleLeaksNoSecretAnywhere(t *testing.T) {
	_, raw := buildTestBundle(t)

	for _, secret := range []string{fakeKey, fakeToken} {
		if bytes.Contains(raw, []byte(secret)) {
			t.Fatalf("bundle bytes contain a secret (%s…)", secret[:12])
		}
	}
}

func TestBundleRedactsInlineSecretsInConfigAndLogs(t *testing.T) {
	entries, _ := buildTestBundle(t)

	settings := entries["config/settings.yaml"]
	if strings.Contains(settings, fakeToken) {
		t.Fatalf("settings.yaml leaked an inline token: %s", settings)
	}
	if !strings.Contains(settings, "posture: balanced") {
		t.Fatalf("settings.yaml lost its readable content: %s", settings)
	}

	logs := entries["logs/sidecar.log"]
	if strings.Contains(logs, fakeKey) {
		t.Fatalf("log leaked a key: %s", logs)
	}
	if !strings.Contains(logs, "ready") {
		t.Fatalf("log lost its readable content: %s", logs)
	}
}

// Redaction keeps the version and probe facts a report needs.
func TestBundleKeepsTheFactsAReportNeeds(t *testing.T) {
	entries, _ := buildTestBundle(t)

	health := entries["health.json"]
	for _, want := range []string{"0.1.0", "schema_version"} {
		if !strings.Contains(health, want) {
			t.Fatalf("health.json lost %q: %s", want, health)
		}
	}

	preflight := entries["preflight.json"]
	for _, want := range []string{"git_engine", "degraded"} {
		if !strings.Contains(preflight, want) {
			t.Fatalf("preflight.json lost %q: %s", want, preflight)
		}
	}
}

func TestManifestStatesNoUpload(t *testing.T) {
	entries, _ := buildTestBundle(t)

	manifest := entries["manifest.json"]
	if !strings.Contains(manifest, "none") || !strings.Contains(manifest, "2026-07-25") ||
		!strings.Contains(manifest, "layered diagnostics scrubber") {
		t.Fatalf("manifest missing upload posture or timestamp: %s", manifest)
	}
	if strings.Contains(manifest, "internal/observability") {
		t.Fatalf("manifest names an internal redactor: %s", manifest)
	}
}

func TestBundleToleratesMissingDirectories(t *testing.T) {
	raw, err := Build(t.Context(), Input{
		Health:      map[string]any{"status": "ok"},
		Preflight:   map[string]any{"overall": "ok"},
		ConfigDir:   filepath.Join(t.TempDir(), "does-not-exist"),
		LogDir:      filepath.Join(t.TempDir(), "also-missing"),
		GeneratedAt: fixedTime(),
	})
	if err != nil {
		testutil.FailErr(t, "build bundle with missing dirs", err)
	}
	if len(raw) == 0 {
		t.Fatal("bundle is empty")
	}
}

func TestBundleTruncatesHugeLogs(t *testing.T) {
	logDir := t.TempDir()
	big := strings.Repeat("x", maxLogTailBytes*2) + "TAIL_MARKER"
	if err := os.WriteFile(filepath.Join(logDir, "big.log"), []byte(big), 0o600); err != nil {
		testutil.FailErr(t, "write big log", err)
	}

	raw, err := Build(t.Context(), Input{Health: map[string]any{}, Preflight: map[string]any{}, LogDir: logDir, GeneratedAt: fixedTime()})
	if err != nil {
		testutil.FailErr(t, "build bundle", err)
	}

	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		testutil.FailErr(t, "open zip", err)
	}
	for _, f := range zr.File {
		if f.Name != "logs/big.log" {
			continue
		}
		rc, _ := f.Open()
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(rc)
		_ = rc.Close()

		if int64(buf.Len()) > maxLogTailBytes {
			t.Fatalf("log entry is %d bytes, want at most %d", buf.Len(), maxLogTailBytes)
		}
		if !strings.Contains(buf.String(), "TAIL_MARKER") {
			t.Fatal("truncation kept the head instead of the tail")
		}
		return
	}
	t.Fatal("logs/big.log not found in bundle")
}

func TestBuildStartupUsesTheUsualRedactedBundleShape(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "engine.log.1"), []byte("Authorization: Bearer "+fakeKey+"\nstartup failed\n"), 0o600); err != nil {
		testutil.FailErr(t, "write rotated engine log", err)
	}
	const startupProviderSecret = "startup-provider-secret"
	if err := os.WriteFile(filepath.Join(dir, "settings.yaml"), []byte("OPENAI_API_KEY: "+startupProviderSecret+"\nposture: balanced\n"), 0o600); err != nil {
		testutil.FailErr(t, "write startup settings", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "credential-vault.age"), []byte("should-not-be-in-the-archive\n"), 0o600); err != nil {
		testutil.FailErr(t, "write startup credentials", err)
	}

	raw, err := BuildStartup(t.Context(), dir, fixedTime())
	testutil.FailErr(t, "build startup bundle", err)
	for _, secret := range []string{fakeKey, startupProviderSecret, "should-not-be-in-the-archive"} {
		if bytes.Contains(raw, []byte(secret)) {
			t.Fatalf("startup bundle leaked a secret (%q)", secret)
		}
	}

	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	testutil.FailErr(t, "open startup bundle", err)
	entries := make(map[string]string, len(zr.File))
	for _, f := range zr.File {
		rc, openErr := f.Open()
		testutil.FailErr(t, "open startup entry "+f.Name, openErr)
		var buf bytes.Buffer
		_, readErr := buf.ReadFrom(rc)
		_ = rc.Close()
		testutil.FailErr(t, "read startup entry "+f.Name, readErr)
		entries[f.Name] = buf.String()
	}

	for _, want := range []string{"manifest.json", "health.json", "preflight.json", "config/settings.yaml", "logs/engine.log.1"} {
		if _, ok := entries[want]; !ok {
			t.Fatalf("startup bundle is missing %s (entries: %v)", want, keys(entries))
		}
	}
	if !strings.Contains(entries["health.json"], "engine_not_started") {
		t.Fatalf("startup health does not explain its scope: %s", entries["health.json"])
	}
	if _, ok := entries["config/credential-vault.age"]; ok {
		t.Fatal("startup bundle included the credential vault")
	}
}

func TestDiagnosticsRedactorCoversEveryBundledProviderCredential(t *testing.T) {
	redactor, err := NewRedactor()
	testutil.FailErr(t, "build diagnostics redactor", err)
	names, err := providerSecretNames()
	testutil.FailErr(t, "load bundled provider credential names", err)
	if len(names) == 0 {
		t.Fatal("expected bundled provider credentials")
	}

	var input strings.Builder
	for i, name := range names {
		value := fmt.Sprintf("provider-secret-%03d", i)
		switch i % 4 {
		case 0:
			input.WriteString(name + "=\"" + value + "\"\n")
		case 1:
			input.WriteString(name + ": " + value + "\n")
		case 2:
			input.WriteString("\"" + name + "\": \"" + value + "\"\n")
		default:
			input.WriteString("'" + name + "': '" + value + "'\n")
		}
	}
	out := redactor.RedactText(t.Context(), input.String())
	for i, name := range names {
		value := fmt.Sprintf("provider-secret-%03d", i)
		if strings.Contains(out, value) {
			t.Errorf("provider credential %s leaked from diagnostics redactor", name)
		}
	}
}

// Structured bundle entries use catalog-backed redaction.
func TestBundleRedactsCatalogCredentialsFromStructuredEntries(t *testing.T) {
	const healthSecret = "health-provider-secret"
	const preflightSecret = "preflight-provider-secret"
	redactor, err := NewRedactor()
	testutil.FailErr(t, "build diagnostics redactor", err)
	raw, err := Build(t.Context(), Input{
		Health: map[string]any{
			"status":         "ok",
			"OPENAI_API_KEY": healthSecret,
		},
		Preflight: map[string]any{
			"overall":        "degraded",
			"GEMINI_API_KEY": preflightSecret,
		},
		GeneratedAt: fixedTime(),
		Redactor:    redactor,
	})
	testutil.FailErr(t, "build structured diagnostics bundle", err)
	for _, secret := range []string{healthSecret, preflightSecret} {
		if bytes.Contains(raw, []byte(secret)) {
			t.Fatalf("structured diagnostics entry leaked %q", secret)
		}
	}
	entries := bundleEntries(t, raw)
	for _, name := range []string{"health.json", "preflight.json"} {
		if !strings.Contains(entries[name], "[REDACTED]") {
			t.Fatalf("%s did not record a redaction: %s", name, entries[name])
		}
	}
}

func bundleEntries(t *testing.T, raw []byte) map[string]string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	testutil.FailErr(t, "open diagnostics zip", err)
	entries := make(map[string]string, len(zr.File))
	for _, f := range zr.File {
		rc, openErr := f.Open()
		testutil.FailErr(t, "open diagnostics entry "+f.Name, openErr)
		var buf bytes.Buffer
		_, readErr := buf.ReadFrom(rc)
		_ = rc.Close()
		testutil.FailErr(t, "read diagnostics entry "+f.Name, readErr)
		entries[f.Name] = buf.String()
	}
	return entries
}

func keys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestDiagnosticsSecretCanaryExclusion(t *testing.T) {
	t.Parallel()
	configDir := t.TempDir()
	canaries := map[string]string{"unclassified-rogue.yaml": "CANARY-SECRET-unclassified-rogue.yaml"}
	for _, e := range localdata.AllClassifiedEntries() {
		if !e.IsDir && !localdata.IsDiagnosticsAllowed(e.Name) {
			canaries[e.Name] = "CANARY-SECRET-" + e.Name
		}
	}
	for name, content := range canaries {
		testutil.FailErr(t, "write excluded fixture "+name, os.WriteFile(filepath.Join(configDir, name), []byte(content), 0o600))
	}
	testutil.FailErr(t, "write allowed fixture settings.yaml",
		os.WriteFile(filepath.Join(configDir, "settings.yaml"), []byte("posture: balanced\n"), 0o600))

	redactor, err := NewRedactor()
	testutil.FailErr(t, "new redactor", err)
	raw, err := Build(t.Context(), Input{
		Health:      map[string]any{"status": "ok"},
		Preflight:   map[string]any{"overall": "ok"},
		ConfigDir:   configDir,
		LogDir:      t.TempDir(),
		GeneratedAt: fixedTime(),
		Redactor:    redactor,
	})
	testutil.FailErr(t, "build bundle", err)

	entries := bundleEntries(t, raw)
	if _, ok := entries["config/settings.yaml"]; !ok {
		t.Fatalf("allowed config file is missing (entries: %v)", keys(entries))
	}
	for name, canary := range canaries {
		if _, ok := entries["config/"+name]; ok {
			t.Errorf("diagnostics bundle contains excluded config file %s", name)
		}
		if bytes.Contains(raw, []byte(canary)) {
			t.Errorf("diagnostics bundle leaked the contents of %s", name)
		}
	}
}

func TestBundleIncludesResolvedBoundaryOverrides(t *testing.T) {
	t.Setenv("LYCAON_SANDBOX_DENY_READ", "off")
	t.Setenv("LYCAON_SANDBOX_WRITE_ROOTS", t.TempDir())
	entries, _ := buildTestBundle(t)
	var overrides struct {
		ControlPlaneReadsAllowed bool     `json:"control_plane_reads_allowed"`
		AdditionalWriteRoots     []string `json:"additional_write_roots"`
	}
	testutil.FailErr(t, "decode boundary overrides", json.Unmarshal([]byte(entries["boundary-overrides.json"]), &overrides))
	if !overrides.ControlPlaneReadsAllowed || len(overrides.AdditionalWriteRoots) != 1 {
		t.Fatalf("boundary overrides = %+v", overrides)
	}
}
