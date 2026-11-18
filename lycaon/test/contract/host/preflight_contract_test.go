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

func TestPreflightProbeIDsAreStableAndOrdered(t *testing.T) {
	t.Parallel()
	want := []string{
		"os_version", "config_dir", "disk_space", "command_path", "git_engine",
		"scanner_engine", "browser_engine", "decision_engine", "ocr_engine", "provider_configured", "lite_slot",
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
		OSProductVer:  func() (string, error) { return "1.0", nil },
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
