// Package browserengine resolves and provisions the pinned headless browser.
package browserengine

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/configlayout"
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
	for _, root := range bundleRoots() {
		if bin := BundledBinary(root); bin != "" {
			return ResolvedBrowser{Path: bin, Source: SourceBundle}, true
		}
	}
	if managed := ManagedBinary(opts.CacheDir); managed != "" {
		return ResolvedBrowser{Path: managed, Source: SourceManaged}, true
	}
	return ResolvedBrowser{}, false
}

// BundledBinary returns a runnable browser in an engine resource root.
func BundledBinary(root string) string {
	if root == "" {
		return ""
	}
	bin := filepath.Join(root, "browser", headlessShellName())
	if installReady(bin) {
		return bin
	}
	return ""
}

func bundleRoots() []string {
	roots := []string{configlayout.EngineRoot()}
	exe, err := os.Executable()
	if err != nil {
		return roots
	}
	if exe, err := filepath.EvalSymlinks(exe); err == nil {
		roots = append(roots, filepath.Dir(exe))
	}
	return roots
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
