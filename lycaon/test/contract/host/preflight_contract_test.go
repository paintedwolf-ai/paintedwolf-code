package contract

import (
	"context"
	"errors"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/gitengine"
	"github.com/lycaon/lycaon/internal/preflight"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/usernotice"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// preflightProbeCodes lists every emitted probe code.
var preflightProbeCodes = []string{
	preflight.CodeOSBelowFloor,
	preflight.CodeConfigDirUnwritable,
	preflight.CodeHostFileLimitReached,
	preflight.CodeDiskSpaceLow,
	preflight.CodeCommandPathLimited,
	preflight.CodeGitEngineUnavailable,
	preflight.CodeScannerEngineUnavailable,
	preflight.CodeBrowserEngineUnavailable,
	preflight.CodeDecisionEngineUnavailable,
	preflight.CodeNoProviderConfigured,
	preflight.CodeLiteUnavailable,
}

func TestPreflightProbeCodesHaveNotices(t *testing.T) {
	t.Parallel()
	cfg := loadUserNoticeConfig(t)

	for _, code := range preflightProbeCodes {
		entry, ok := cfg.UserNotices[code]
		if !ok {
			t.Fatalf("preflight code %q has no user-notice entry: the probe would report something the user cannot read", code)
		}
		if !entry.IsUserVisible() {
			t.Fatalf("preflight code %q is user_visible:false", code)
		}
		if !entry.HasSurface("preflight") {
			t.Fatalf("preflight code %q is missing surfaces: [preflight]", code)
		}
	}
}

// TestPreflightNoticesCarrySuggestedAction checks actionable notice copy.
func TestPreflightNoticesCarrySuggestedAction(t *testing.T) {
	t.Parallel()
	cfg := loadUserNoticeConfig(t)

	for _, code := range preflightProbeCodes {
		entry := cfg.UserNotices[code]
		copy := usernotice.RenderCopy(entry, nil)
		if strings.TrimSpace(copy.SuggestedAction) == "" {
			t.Fatalf("preflight code %q has no suggested_action: preflight reports and never remediates, "+
				"so the copy is the only remedy the user gets", code)
		}
		if strings.TrimSpace(copy.Title) == "" {
			t.Fatalf("preflight code %q has no title", code)
		}
	}
}

// TestPreflightSurfaceHasNoOrphanCodes checks probe and catalog parity.
func TestPreflightSurfaceHasNoOrphanCodes(t *testing.T) {
	t.Parallel()
	cfg := loadUserNoticeConfig(t)

	declared := map[string]bool{}
	for _, code := range preflightProbeCodes {
		declared[code] = true
	}

	for _, code := range usernotice.PreflightCodes(cfg) {
		if !declared[code] {
			t.Fatalf("user-notice code %q claims surfaces: [preflight] but no probe emits it", code)
		}
	}
}

// TestPreflightNoticesAreAppScoped keeps readiness copy renderable: CriticalStop
// and PreflightNudge render machine-scoped results only, so a project-scoped
// probe would publish copy no surface shows.
func TestPreflightNoticesAreAppScoped(t *testing.T) {
	t.Parallel()
	cfg := loadUserNoticeConfig(t)

	for _, code := range usernotice.PreflightCodes(cfg) {
		entry := cfg.UserNotices[code]
		if entry.Notification == nil {
			continue
		}
		for _, res := range entry.Notification.Normalized() {
			if res.Scope != usernotice.ScopeApp {
				t.Fatalf("preflight code %q resolution %q declares scope %q: readiness renders on app scope only",
					code, res.ID, res.Scope)
			}
		}
	}
}

func TestPreflightProbeIDsAreStableAndOrdered(t *testing.T) {
	t.Parallel()
	want := []string{
		"os_version", "config_dir", "disk_space", "command_path", "git_engine",
		"scanner_engine", "browser_engine", "decision_engine", "provider_configured", "lite_slot",
	}

	got := preflight.Default().IDs()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("preflight probe ids = %v, want %v — ids are wire-visible and their order is the display order", got, want)
	}
}

// TestPreflightNeverLeaksHomePath checks wire-visible probe details.
func TestPreflightNeverLeaksHomePath(t *testing.T) {
	t.Parallel()
	home, err := os.UserHomeDir()
	if err != nil {
		testutil.FailErr(t, "resolve home dir", err)
	}

	// Force every probe failure path.
	configDir := filepath.Join(home, ".config", "lycaon-preflight-contract")
	env := preflight.Env{
		ConfigDir:     configDir,
		FreeBytes:     func(string) (uint64, error) { return 1 << 20, nil },
		OSProductVer:  func() (string, error) { return "10.15", nil },
		ProviderCount: func() int { return 0 },
		ResolveGitEngine: func() error {
			return &gitengine.UnavailableError{Reason: "missing"}
		},
		ResolveScanner: func() (string, string, error) { return "", "checksum", errFailProbe },
		CheckBrowser: func(context.Context) (preflight.BrowserReason, error) {
			return preflight.ReasonBrowserManagedCacheMissing, errFailProbe
		},
	}
	t.Cleanup(func() { _ = os.RemoveAll(configDir) })

	results, _ := preflight.Default().Run(context.Background(), env)

	for _, res := range results {
		for key, value := range res.Detail {
			if strings.Contains(value, home) {
				t.Fatalf("probe %s detail %q leaks the user's home directory: %q", res.ID, key, value)
			}
			if strings.HasPrefix(value, "/") {
				t.Fatalf("probe %s detail %q is an absolute path (%q); details are facts, not paths", res.ID, key, value)
			}
		}
	}
}

// TestPreflightPackageMakesNoNetworkCalls keeps readiness probes local.
func TestPreflightPackageMakesNoNetworkCalls(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(contractcheck.RepoRoot(t), "lycaon", "internal", "preflight")

	entries, err := os.ReadDir(dir)
	if err != nil {
		testutil.FailErr(t, "read preflight package dir", err)
	}

	fset := token.NewFileSet()
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(dir, entry.Name()), nil, parser.ImportsOnly)
		if err != nil {
			testutil.FailErr(t, "parse "+entry.Name(), err)
		}
		for _, imp := range file.Imports {
			path := strings.Trim(imp.Path.Value, `"`)
			if path == "net/http" || path == "net" || strings.HasSuffix(path, "/httpclient") {
				t.Fatalf("%s imports %q: preflight reads local machine state only — network reachability is a separate concern",
					entry.Name(), path)
			}
		}
	}
}

// errFailProbe drives probes down their failure paths in the leak test.
var errFailProbe = errors.New("preflight contract: forced failure")
