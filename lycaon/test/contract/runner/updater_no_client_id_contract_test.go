package contract

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func staticUpdaterEndpoint(t *testing.T, root string) string {
	t.Helper()
	conf := contractcheck.ReadRepoFile(t, root, "lycaon-den/src-tauri/tauri.conf.json")
	var parsed struct {
		Plugins struct {
			Updater struct {
				Endpoints []string `json:"endpoints"`
			} `json:"updater"`
		} `json:"plugins"`
	}
	if err := json.Unmarshal([]byte(conf), &parsed); err != nil {
		t.Fatalf("parse tauri updater config: %v", err)
	}
	if len(parsed.Plugins.Updater.Endpoints) != 1 {
		t.Fatalf("tauri updater endpoints = %v, want exactly one", parsed.Plugins.Updater.Endpoints)
	}
	return parsed.Plugins.Updater.Endpoints[0]
}

type updaterScanKind int

const (
	scanUpdaterClient updaterScanKind = iota
	scanReleaseScript
)

var clientIDPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)install-id`),
	regexp.MustCompile(`(?i)machine-id`),
	regexp.MustCompile(`(?i)client-id`),
	regexp.MustCompile(`(?i)x-client-id`),
	regexp.MustCompile(`(?i)rollout-bucket`),
}

var clientHashPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)hash(?:es|ed|ing)?\s*(?:a\s+)?(?:stable\s+)?(?:device|machine|install)\s*id`),
	regexp.MustCompile(`(?i)(?:device|machine|install)Id.*(?:hash|digest|bucket)`),
	regexp.MustCompile(`(?i)rollout\s*percentage`),
	regexp.MustCompile(`(?i)percentage\s*rollout`),
}

func findClientIDViolations(content string, kind updaterScanKind) []string {
	var hits []string
	for _, re := range clientIDPatterns {
		if re.MatchString(content) {
			hits = append(hits, re.String())
		}
	}
	if kind == scanUpdaterClient {
		for _, re := range clientHashPatterns {
			if re.MatchString(content) {
				hits = append(hits, re.String())
			}
		}
	}
	return hits
}

func TestFindClientIDViolationsNegativeControl(t *testing.T) {
	t.Parallel()
	clean := `export async function checkForUpdate() { return invoke("check_update"); }`
	if hits := findClientIDViolations(clean, scanUpdaterClient); len(hits) != 0 {
		t.Fatalf("clean updater client snippet flagged: %v", hits)
	}

	dirty := clean + "\nheaders: { \"x-client-id\": installId }\n"
	if hits := findClientIDViolations(dirty, scanUpdaterClient); len(hits) == 0 {
		t.Fatal("expected x-client-id injection to fail the scanner")
	}

	scriptOK := "bunx wrangler r2 object put \"${R2_BUCKET}/updates/stable/latest.json\" --file latest.json --remote\n"
	if hits := findClientIDViolations(scriptOK, scanReleaseScript); len(hits) != 0 {
		t.Fatalf("R2_BUCKET storage config must not fail the scanner: %v", hits)
	}

	scriptBad := scriptOK + "curl -H 'X-Client-Id: $(machine-id)' https://example.test/update\n"
	if hits := findClientIDViolations(scriptBad, scanReleaseScript); len(hits) == 0 {
		t.Fatal("expected client-id transmission in a release script to fail the scanner")
	}
}

func TestUpdaterNoClientIDInTree(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)

	clientFiles := []string{
		"lycaon-den/src/settings/system/update-service.ts",
		"lycaon-den/src/settings/system/update-error.ts",
		"lycaon-den/src/components/settings/system/UpdatesSettingsPanel.tsx",
		"lycaon-den/src/components/update/UpdatePanel.tsx",
		"lycaon-den/src-tauri/src/update_service.rs",
		"lycaon-den/src-tauri/tauri.conf.json",
	}
	for _, rel := range clientFiles {
		body := contractcheck.ReadRepoFile(t, root, rel)
		if strings.HasSuffix(rel, ".rs") {
			body = contractcheck.RustModuleSource(t, root, rel)
		}
		if hits := findClientIDViolations(body, scanUpdaterClient); len(hits) != 0 {
			t.Fatalf("%s carries updater client-id / rollout telemetry (%v)", rel, hits)
		}
	}

	scriptGlobs := []string{
		"scripts/release-*.sh",
		"scripts/upgrade-corpus-*.sh",
	}
	for _, pattern := range scriptGlobs {
		matches, err := filepath.Glob(filepath.Join(root, pattern))
		if err != nil {
			t.Fatalf("glob %s: %v", pattern, err)
		}
		if len(matches) == 0 {
			t.Fatalf("glob %s matched nothing", pattern)
		}
		for _, abs := range matches {
			raw, err := os.ReadFile(abs)
			if err != nil {
				t.Fatalf("read %s: %v", abs, err)
			}
			rel, err := filepath.Rel(root, abs)
			if err != nil {
				rel = abs
			}
			if hits := findClientIDViolations(string(raw), scanReleaseScript); len(hits) != 0 {
				t.Fatalf("%s derives or transmits a client id (%v)", rel, hits)
			}
		}
	}

	endpoint := staticUpdaterEndpoint(t, root)
	nativeService := contractcheck.RustModuleSource(t, root, "lycaon-den/src-tauri/src/update_service.rs")
	if !strings.Contains(nativeService, ".updater_builder()") ||
		!strings.Contains(nativeService, ".download(") ||
		!strings.Contains(nativeService, ".install(") {
		t.Fatal("native update service must own check, verified download, and install")
	}
	if !strings.Contains(nativeService, "if automatic && !self.state.checks_enabled") ||
		!strings.Contains(nativeService, "write_preferences(") {
		t.Fatal("native update service must persist and enforce the check preference")
	}
	if !strings.Contains(nativeService, "UPDATE_STATE_EVENT") ||
		!strings.Contains(nativeService, "emit_update_state") {
		t.Fatal("native update service must publish lifecycle state changes")
	}
	clientService := contractcheck.ReadRepoFile(t, root, "lycaon-den/src/settings/system/update-service.ts")
	for _, command := range []string{
		"get_update_state", "set_update_checks_enabled", "set_update_channel", "check_update", "install_update",
	} {
		if !strings.Contains(clientService, `invoke<NativeUpdateState>("`+command+`"`) {
			t.Fatalf("update service does not invoke %s", command)
		}
	}
	if strings.Contains(clientService, endpoint) || strings.Contains(clientService, "fetch(") ||
		strings.Contains(clientService, "plugin-updater") {
		t.Fatal("client update service provides transport")
	}
	packageJSON := contractcheck.ReadRepoFile(t, root, "lycaon-den/package.json")
	if strings.Contains(packageJSON, "@tauri-apps/plugin-updater") ||
		strings.Contains(packageJSON, "@tauri-apps/plugin-process") {
		t.Fatal("browser package contains native update dependencies")
	}
	cask := contractcheck.ReadRepoFile(t, root, "packaging/homebrew/painted-wolf-code.rb.tmpl")
	if !strings.Contains(cask, "install-source.json") ||
		!strings.Contains(cask, `homebrew_cask`) {
		t.Fatal("Homebrew cask must write the explicit package-manager receipt")
	}
	// Stable and preview casks install the same app bundle.
	release := contractcheck.ReadRepoFile(t, root, ".github/workflows/release.yml")
	if !strings.Contains(cask, `conflicts_with cask: "@@CONFLICTS@@"`) ||
		!strings.Contains(release, `-e "s|@@CONFLICTS@@|${conflicts}|g"`) ||
		!strings.Contains(release, `[[ "${token}" == painted-wolf-code ]] && conflicts=painted-wolf-code@preview`) {
		t.Fatal("stable and preview Homebrew casks must declare conflicts_with each other")
	}
	if strings.Contains(nativeService, "Caskroom") ||
		strings.Contains(nativeService, "HOMEBREW_PREFIX") {
		t.Fatal("native update source must not infer installation type from global package-manager paths")
	}
	app := contractcheck.ReadRepoFile(t, root, "lycaon-den/src/App.tsx")
	if !strings.Contains(app, "mountUpdateNotice") {
		t.Fatal("the app shell must surface automatic update discovery")
	}
}

func TestReleaseHaltCLI(t *testing.T) {
	root := contractcheck.RepoRoot(t)
	script := filepath.Join(root, "scripts/release-halt.sh")
	fixtures := filepath.Join(root, "lycaon/testdata/release-halt")
	current := filepath.Join(fixtures, "current-bad.json")
	lastGood := filepath.Join(fixtures, "last-good.json")

	run := func(t *testing.T, args ...string) (string, int) {
		t.Helper()
		cmd := exec.Command("bash", append([]string{script}, args...)...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "DOWNLOAD_BASE_URL=https://downloads.paintedwolf.dev")
		out, err := cmd.CombinedOutput()
		ec := 0
		if err != nil {
			var ee *exec.ExitError
			if errors.As(err, &ee) {
				ec = ee.ExitCode()
			} else {
				t.Fatalf("run %v: %v\n%s", args, err, out)
			}
		}
		return string(out), ec
	}

	t.Run("file_dry_run", func(t *testing.T) {
		out, ec := run(t, "--channel", "stable", "--bad", "0.2.0", "--last-good", "0.1.0",
			"--current-file", current, "--release-file", lastGood, "--dry-run")
		if ec != 0 {
			t.Fatalf("exit %d: %s", ec, out)
		}
		if !strings.Contains(out, "dry-run ok") {
			t.Fatalf("expected dry-run ok: %s", out)
		}
	})

	t.Run("active_version_guard", func(t *testing.T) {
		out, ec := run(t, "--channel", "stable", "--bad", "0.3.0", "--last-good", "0.1.0",
			"--current-file", current, "--release-file", lastGood, "--dry-run")
		if ec != 1 || !strings.Contains(out, "active updater version is 0.2.0") {
			t.Fatalf("want active-version guard: ec=%d out=%s", ec, out)
		}
	})

	t.Run("usage", func(t *testing.T) {
		_, ec := run(t)
		if ec != 2 {
			t.Fatalf("want usage exit 2 got %d", ec)
		}
	})

	t.Run("self_test", func(t *testing.T) {
		out, ec := run(t, "--self-test")
		if ec != 0 {
			t.Fatalf("self-test failed: %d\n%s", ec, out)
		}
	})
}

func TestReleaseHaltWorkflowIsProtected(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	body := contractcheck.ReadRepoFile(t, root, ".github/workflows/release-halt.yml")
	for _, needle := range []string{
		"workflow_dispatch:",
		"environment: release-publication",
		"concurrency:",
		"group: release-static-update",
		"dry_run:",
		"scripts/release_withdrawal.py prepare",
		"scripts/release_withdrawal.py apply",
	} {
		if !strings.Contains(body, needle) {
			t.Fatalf("release halt workflow missing %q", needle)
		}
	}
}
