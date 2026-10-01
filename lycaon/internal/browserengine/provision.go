package browserengine

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/egressclass"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/httpclient"
	"github.com/lycaon/lycaon/internal/sandbox"
)

var ensureMu sync.Mutex

// cftPlatform maps host platforms to artifact IDs.
func cftPlatform() (string, error) {
	return cftPlatformFor(runtime.GOOS, runtime.GOARCH)
}

func cftPlatformFor(goos, goarch string) (string, error) {
	switch goos + "/" + goarch {
	case "darwin/arm64":
		return "mac-arm64", nil
	case "darwin/amd64":
		return "mac-x64", nil
	case "linux/amd64":
		return "linux64", nil
	case "linux/arm64":
		return "linux-arm64", nil
	case "windows/amd64":
		return "win64", nil
	case "windows/386":
		return "win32", nil
	default:
		return "", fmt.Errorf("unsupported platform %s/%s for chrome-headless-shell", goos, goarch)
	}
}

// ManagedCacheDir returns the shared browser cache under engine data.
func ManagedCacheDir() string {
	base, err := configdir.UserConfigDir()
	if err != nil || base == "" {
		return ""
	}
	return enginepaths.BrowserCacheRootUnder(base)
}

// managedRoot resolves the pinned install tree inside an explicit cache.
func managedRoot(cacheDir string) string {
	cacheDir = strings.TrimSpace(cacheDir)
	if cacheDir == "" {
		return ""
	}
	plat, err := cftPlatform()
	if err != nil {
		return ""
	}
	return filepath.Join(cacheDir, "managed", PinnedChromeHeadlessShell, plat)
}

func managedBinaryPath(cacheDir string) string {
	root := managedRoot(cacheDir)
	if root == "" {
		return ""
	}
	return filepath.Join(root, headlessShellName())
}

func managedInstallReady(cacheDir string) bool {
	bin := managedBinaryPath(cacheDir)
	return bin != "" && installReady(bin)
}

// EnsureBinary resolves env → bundle → managed, provisioning the managed cache
// when AllowDownload permits. A miss is a typed refusal; there is no
// system-browser step.
func EnsureBinary(ctx context.Context, opts ResolveOptions) (ResolvedBrowser, error) {
	if resolved, ok := ResolveBinary(opts); ok {
		return resolved, nil
	}

	var provisionErr error
	if opts.AllowDownload && !skipDownload() {
		path, err := ensureManaged(ctx, opts.CacheDir)
		if err == nil {
			return ResolvedBrowser{Path: path, Source: SourceManaged}, nil
		}
		provisionErr = err
	}

	// The shipped app carries its own tree, so a miss there is a broken install
	// and gets a distinct code from a failed provision.
	if !opts.AllowDownload {
		return ResolvedBrowser{}, Reject("BROWSER_ENGINE_UNAVAILABLE", map[string]any{
			"reason": "no_bundled_browser",
		})
	}

	reason := "no_browser"
	if provisionErr != nil {
		reason = "provision_failed"
	}
	return ResolvedBrowser{}, Reject("BROWSER_UNAVAILABLE", map[string]any{
		"reason": reason,
	})
}

func ensureManaged(ctx context.Context, cacheDir string) (string, error) {
	ensureMu.Lock()
	defer ensureMu.Unlock()

	if managedInstallReady(cacheDir) {
		return managedBinaryPath(cacheDir), nil
	}
	plat, err := cftPlatform()
	if err != nil {
		return "", err
	}
	root := managedRoot(cacheDir)
	if root == "" {
		return "", fmt.Errorf("browser cache dir unavailable")
	}
	if err := os.MkdirAll(root, 0o755); err != nil { //nolint:gosec // G301 — managed browser cache under ~/.config/paintedwolf
		return "", err
	}

	version := PinnedChromeHeadlessShell
	url := fmt.Sprintf("%s/%s/%s/chrome-headless-shell-%s.zip",
		chromeForTestingBase, version, plat, plat)

	tmpZip, err := os.CreateTemp("", "lycaon-chrome-headless-shell-*.zip")
	if err != nil {
		return "", err
	}
	zipPath := tmpZip.Name()
	defer func() { _ = os.Remove(zipPath) }()

	if err := downloadFile(ctx, url, tmpZip); err != nil {
		_ = tmpZip.Close()
		return "", err
	}
	if err := tmpZip.Close(); err != nil {
		return "", err
	}
	if err := verifyPinnedArchive(zipPath, plat); err != nil {
		return "", err
	}

	staging, err := os.MkdirTemp(filepath.Dir(root), "staging-*")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(staging) }()

	if err := unzipFlat(zipPath, staging); err != nil {
		return "", err
	}
	srcBin, err := findHeadlessShell(staging)
	if err != nil {
		return "", err
	}
	srcDir := filepath.Dir(srcBin)
	if err := mirrorDir(srcDir, root); err != nil {
		return "", err
	}
	dstBin := filepath.Join(root, headlessShellName())
	if runtime.GOOS != "windows" {
		if err := os.Chmod(dstBin, 0o755); err != nil { //nolint:gosec // G302 — chrome-headless-shell must be executable
			return "", err
		}
	}
	if runtime.GOOS == "darwin" {
		_ = clearMacQuarantine(dstBin)
	}
	marker := filepath.Join(root, ".complete")
	if err := os.WriteFile(marker, []byte(version+"\n"), 0o644); err != nil { //nolint:gosec // G306 — version marker is non-secret
		return "", err
	}
	if !isExecutable(dstBin) {
		return "", fmt.Errorf("provisioned browser is not executable: %s", dstBin)
	}
	pruneSupersededManaged(ctx, cacheDir, version)
	return dstBin, nil
}

// pruneSupersededManaged keeps one install tree: the pinned version.
func pruneSupersededManaged(ctx context.Context, cacheDir, keep string) {
	managed := filepath.Join(strings.TrimSpace(cacheDir), "managed")
	entries, err := os.ReadDir(managed)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() == keep {
			continue
		}
		path := filepath.Join(managed, entry.Name())
		if err := os.RemoveAll(path); err != nil {
			slog.WarnContext(ctx, "superseded managed browser not removed", "path", path, "err", err)
			continue
		}
		slog.InfoContext(ctx, "removed superseded managed browser", "path", path)
	}
}

// verifyPinnedArchive checks the downloaded zip against the per-platform
// sha256 pin before anything is unpacked. Fail closed: no pin for the
// platform is as fatal as a mismatch.
func verifyPinnedArchive(zipPath, plat string) error {
	want, ok := pinnedChromeHeadlessShellSHA256[plat]
	if !ok || want == "" {
		return fmt.Errorf("no pinned sha256 for chrome-headless-shell %s on %s: refusing unverified archive",
			PinnedChromeHeadlessShell, plat)
	}
	f, err := os.Open(zipPath)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	got := hex.EncodeToString(h.Sum(nil))
	if !strings.EqualFold(got, want) {
		return fmt.Errorf("chrome-headless-shell %s %s sha256 mismatch: expected %s, actual %s",
			PinnedChromeHeadlessShell, plat, want, got)
	}
	return nil
}

func downloadFile(ctx context.Context, url string, w io.Writer) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	client := httpclient.Download(egressclass.BrowserEngineDownload, 10*time.Minute)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: HTTP %d", url, resp.StatusCode)
	}
	_, err = io.Copy(w, resp.Body)
	return err
}

func unzipFlat(zipPath, dest string) error {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer func() { _ = r.Close() }()
	for _, f := range r.File {
		if filepath.IsAbs(f.Name) || sandbox.HasParentTraversal(f.Name) {
			return fmt.Errorf("zip entry escapes: %s", f.Name)
		}
		name := filepath.Clean(f.Name)
		target := filepath.Join(dest, name)
		if !strings.HasPrefix(target, filepath.Clean(dest)+string(os.PathSeparator)) && target != filepath.Clean(dest) {
			return fmt.Errorf("zip entry escapes: %s", f.Name)
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil { //nolint:gosec // G301 — CfT zip staging dirs
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil { //nolint:gosec // G301 — CfT zip staging dirs
			return err
		}
		if err := extractZipFile(f, target); err != nil {
			return err
		}
	}
	return nil
}

func extractZipFile(f *zip.File, target string) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer func() { _ = rc.Close() }()
	out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, f.Mode())
	if err != nil {
		return err
	}
	defer func() { _ = out.Close() }()
	_, err = io.Copy(out, rc) //nolint:gosec // G110 — pinned archive with hash verification
	return err
}

func findHeadlessShell(root string) (string, error) {
	want := headlessShellName()
	var found string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		if info.Name() == want {
			found = path
			return io.EOF
		}
		return nil
	})
	if found != "" {
		return found, nil
	}
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return "", fmt.Errorf("chrome-headless-shell binary not found in archive")
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755) //nolint:gosec // G302 — preserved executable bits for CfT helpers
	if err != nil {
		return err
	}
	defer func() { _ = out.Close() }()
	_, err = io.Copy(out, in)
	return err
}

// mirrorDir copies sibling resources into a flat runtime directory.
func mirrorDir(src, dst string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dst, 0o755); err != nil { //nolint:gosec // G301 — managed CfT install tree
		return err
	}
	for _, e := range entries {
		from := filepath.Join(src, e.Name())
		to := filepath.Join(dst, e.Name())
		if e.IsDir() {
			if err := copyTree(from, to); err != nil {
				return err
			}
			continue
		}
		if err := copyFile(from, to); err != nil {
			return err
		}
		info, err := e.Info()
		if err == nil && info.Mode()&0o111 != 0 && runtime.GOOS != "windows" {
			_ = os.Chmod(to, info.Mode()|0o111)
		}
	}
	return nil
}

func copyTree(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755) //nolint:gosec // G301 — managed CfT install tree
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil { //nolint:gosec // G301 — managed CfT install tree
			return err
		}
		if err := copyFile(path, target); err != nil {
			return err
		}
		if info.Mode()&0o111 != 0 && runtime.GOOS != "windows" {
			_ = os.Chmod(target, info.Mode()|0o111)
		}
		return nil
	})
}
