// Package browserengine resolves and provisions the pinned headless browser.
package browserengine

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/enginepaths"
)

// EnvBrowserBin selects an executable with the required sibling resources.
const EnvBrowserBin = "LYCAON_BROWSER_BIN"

// EnvBrowserSkipDownload prevents managed browser downloads.
const EnvBrowserSkipDownload = "LYCAON_BROWSER_SKIP_DOWNLOAD"

// EnvBrowserIntegration lets a test provision the managed cache over the network.
const EnvBrowserIntegration = "LYCAON_BROWSER_INTEGRATION"

// EnvBrowserRequired fails a test that resolves no browser instead of skipping it.
const EnvBrowserRequired = "LYCAON_BROWSER_REQUIRED"

// Source labels where ResolveBinary found a browser.
const (
	SourceEnv     = "env"
	SourceBundle  = "bundle"
	SourceManaged = "managed"
)

// ResolveOptions controls browser binary discovery.
type ResolveOptions struct {
	// CacheDir is both the managed cache root and sandbox write root.
	CacheDir string
	// AllowDownload lets explicit setup fetch the managed browser artifact.
	AllowDownload bool
}

// ResolvedBrowser is a discovered headless browser binary.
type ResolvedBrowser struct {
	Path   string
	Source string
}

// ResolveBinary checks installed browsers without downloading.
func ResolveBinary(opts ResolveOptions) (ResolvedBrowser, bool) {
	if path := strings.TrimSpace(os.Getenv(EnvBrowserBin)); path != "" {
		if installReady(path) {
			return ResolvedBrowser{Path: path, Source: SourceEnv}, true
		}
	}
	for _, cand := range bundleCandidates() {
		if installReady(cand) {
			return ResolvedBrowser{Path: cand, Source: SourceBundle}, true
		}
	}
	if managed := managedBinaryPath(opts.CacheDir); managed != "" && installReady(managed) {
		return ResolvedBrowser{Path: managed, Source: SourceManaged}, true
	}
	return ResolvedBrowser{}, false
}

// EnvTestHostHome is the developer's home directory, exported by test isolation before it
// replaces HOME, so a test can use the browser the developer already provisioned.
const EnvTestHostHome = "PW_TEST_HOST_HOME"

// TestingEnableBundledBinary points tests at a staged browser, or else at the pinned browser
// provisioned in the developer's own cache.
func TestingEnableBundledBinary() {
	if strings.TrimSpace(os.Getenv(EnvBrowserBin)) != "" {
		return
	}
	bin := findStagedHeadlessShell()
	if bin == "" {
		bin = hostProvisionedHeadlessShell()
	}
	if bin == "" {
		return
	}
	_ = os.Setenv(EnvBrowserBin, bin)
}

// hostProvisionedHeadlessShell is the pinned, hash-verified browser `browser:ensure`
// installed under the developer's home, when isolation reports that home.
func hostProvisionedHeadlessShell() string {
	home := strings.TrimSpace(os.Getenv(EnvTestHostHome))
	if home == "" {
		return ""
	}
	bin := managedBinaryPath(enginepaths.BrowserCacheRootUnder(configdir.ChannelConfigRoot(home)))
	if bin == "" || !installReady(bin) {
		return ""
	}
	return bin
}

func findStagedHeadlessShell() string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	for {
		candidate := filepath.Join(dir, "lycaon-den", "src-tauri", "engine-root", "browser", headlessShellName())
		if installReady(candidate) {
			return candidate
		}
		if _, err := os.Stat(filepath.Join(dir, "Taskfile.yml")); err == nil {
			return ""
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

func bundleCandidates() []string {
	name := headlessShellName()
	var cands []string
	if root := configlayout.EngineRoot(); root != "" {
		cands = append(cands, filepath.Join(root, "browser", name))
	}
	exe, err := os.Executable()
	if err != nil {
		return cands
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return cands
	}
	dir := filepath.Dir(exe)
	cands = append(cands, filepath.Join(dir, "browser", name))
	out := make([]string, 0, len(cands))
	for _, c := range cands {
		out = append(out, filepath.Clean(c))
	}
	return out
}

func headlessShellName() string {
	if runtime.GOOS == "windows" {
		return "chrome-headless-shell.exe"
	}
	return "chrome-headless-shell"
}

func isExecutable(path string) bool {
	st, err := os.Stat(path) //nolint:gosec // G703 — candidates are fixed env/bundle/managed locations
	if err != nil || st.IsDir() {
		return false
	}
	if runtime.GOOS == "windows" {
		return true
	}
	return st.Mode()&0o111 != 0
}

// installReady requires the executable and its sibling resources.
func installReady(bin string) bool {
	if !isExecutable(bin) {
		return false
	}
	_, err := os.Stat(filepath.Join(filepath.Dir(bin), "icudtl.dat")) //nolint:gosec // G703 — fixed sibling of a validated candidate
	return err == nil
}

func skipDownload() bool {
	return configdir.EnvTruthy(os.Getenv(EnvBrowserSkipDownload))
}
