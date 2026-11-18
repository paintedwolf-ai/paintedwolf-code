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
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// TestPreflightNeverLeaksHomePath drives every injectable probe down its
// failure path under a temporary HOME and requires no detail to carry a path.
func TestPreflightNeverLeaksHomePath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// A file where the config directory's parent belongs makes mkdir fail.
	blocker := filepath.Join(home, ".config")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		testutil.FailErr(t, "write config blocker", err)
	}
	env := preflight.Env{
		ConfigDir:     filepath.Join(blocker, "lycaon-preflight-contract"),
		FreeBytes:     func(string) (uint64, error) { return 1 << 20, nil },
		OSProductVer:  func() (string, error) { return "1.0", nil },
		ProviderCount: func() int { return 0 },
		ResolveGitEngine: func() error {
			return &gitengine.UnavailableError{Reason: "missing"}
		},
		ResolveScanner: func() (string, string, error) { return "", "checksum", errFailProbe },
		CheckBrowser: func(context.Context) (preflight.BrowserReason, error) {
			return preflight.ReasonBrowserManagedCacheMissing, errFailProbe
		},
		CheckDecisionEngine: func(context.Context) preflight.DecisionReason {
			return preflight.ReasonDecisionModelMissing
		},
	}
	forced := map[string]bool{
		"os_version": true, "config_dir": true, "disk_space": true, "git_engine": true,
		"scanner_engine": true, "browser_engine": true, "decision_engine": true, "provider_configured": true,
	}

	reg := preflight.Default()
	results, _ := reg.Run(context.Background(), env)
	if len(results) != len(reg.IDs()) {
		t.Fatalf("got %d results for %d probes", len(results), len(reg.IDs()))
	}
	failed := 0
	for _, res := range results {
		if forced[res.ID] {
			if res.Status == preflight.StatusOK || res.Code == "" {
				t.Fatalf("probe %s = %+v, want a failure with a code", res.ID, res)
			}
			failed++
		}
		for key, value := range res.Detail {
			if strings.Contains(value, home) {
				t.Fatalf("probe %s detail %q leaks the user's home directory: %q", res.ID, key, value)
			}
			if strings.HasPrefix(value, "/") {
				t.Fatalf("probe %s detail %q is an absolute path (%q); details are facts, not paths", res.ID, key, value)
			}
		}
	}
	if failed != len(forced) {
		t.Fatalf("%d forced probes failed, want %d", failed, len(forced))
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
