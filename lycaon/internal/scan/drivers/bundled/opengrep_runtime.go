package bundleddriver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/filelock"
)

const (
	opengrepRuntimeFormatVersion = 1
	opengrepRuntimeDirPrefix     = "v1-"
	opengrepRuntimeStagePrefix   = ".stage-"
	opengrepRuntimeLockName      = ".prepare.lock"
	opengrepRuntimeManifestName  = "manifest.json"
	opengrepRuntimeCacheSubdir   = "xdg-cache"
	opengrepRuntimePrepTimeout   = 60 * time.Second
)

var opengrepRuntimeMu sync.RWMutex

type opengrepRuntimeManifest struct {
	Version      int                        `json:"version"`
	BinarySHA256 string                     `json:"binary_sha256"`
	Files        []opengrepRuntimeFileEntry `json:"files"`
}

type opengrepRuntimeFileEntry struct {
	RelPath    string `json:"rel_path"`
	Size       int64  `json:"size"`
	Executable bool   `json:"executable,omitempty"`
	SHA256     string `json:"sha256"`
}

// prepareOpengrepRuntime unpacks and seals the verified bundled OpenGrep
// onefile runtime into the host-owned, read-only runtime store under the user
// config tree. Scan workers receive read-only access to the prepared runtime
// while keeping all mutable scan output inside their ephemeral ActionWorkspace.
func prepareOpengrepRuntime(ctx context.Context, bin string) (string, error) {
	cfgDir, err := configdir.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("resolve runtime store root: %w", err)
	}
	return prepareOpengrepRuntimeInStore(ctx, bin, enginepaths.OpengrepRuntimeRootUnder(cfgDir))
}

func prepareOpengrepRuntimeInStore(ctx context.Context, bin, storeRoot string) (string, error) {
	binarySHA256, err := sha256RegularFile(bin)
	if err != nil {
		return "", fmt.Errorf("hash bundled binary: %w", err)
	}
	if err := os.MkdirAll(storeRoot, 0o700); err != nil {
		return "", fmt.Errorf("create runtime store: %w", err)
	}
	targetDir := filepath.Join(storeRoot, opengrepRuntimeDirPrefix+binarySHA256)
	lockPath := filepath.Join(storeRoot, opengrepRuntimeLockName)

	if cacheDir, ok := tryReusePreparedRuntime(ctx, targetDir, binarySHA256, lockPath); ok {
		return cacheDir, nil
	}

	opengrepRuntimeMu.Lock()
	defer opengrepRuntimeMu.Unlock()

	release, err := acquireRuntimeLock(ctx, lockPath, true)
	if err != nil {
		return "", err
	}
	defer release()

	if verifyPreparedRuntime(targetDir, binarySHA256) == nil {
		return filepath.Join(targetDir, opengrepRuntimeCacheSubdir), nil
	}
	return buildAndPublishRuntime(ctx, bin, binarySHA256, storeRoot, targetDir)
}

func tryReusePreparedRuntime(ctx context.Context, targetDir, binarySHA256, lockPath string) (string, bool) {
	opengrepRuntimeMu.RLock()
	defer opengrepRuntimeMu.RUnlock()

	release, err := acquireRuntimeLock(ctx, lockPath, false)
	if err != nil {
		return "", false
	}
	defer release()

	if verifyPreparedRuntime(targetDir, binarySHA256) != nil {
		return "", false
	}
	return filepath.Join(targetDir, opengrepRuntimeCacheSubdir), true
}

func buildAndPublishRuntime(ctx context.Context, bin, binarySHA256, storeRoot, targetDir string) (string, error) {
	if err := removeReadOnlyTree(targetDir); err != nil {
		return "", fmt.Errorf("clear stale runtime: %w", err)
	}
	stagingDir, err := os.MkdirTemp(storeRoot, opengrepRuntimeStagePrefix+binarySHA256[:12]+"-")
	if err != nil {
		return "", fmt.Errorf("create runtime staging dir: %w", err)
	}
	published := false
	defer func() {
		if !published {
			_ = removeReadOnlyTree(stagingDir)
		}
	}()

	stagingCache := filepath.Join(stagingDir, opengrepRuntimeCacheSubdir)
	stagingTmp := filepath.Join(stagingDir, "tmp")
	if err := os.MkdirAll(stagingCache, 0o700); err != nil {
		return "", fmt.Errorf("create runtime cache dir: %w", err)
	}
	if err := os.MkdirAll(stagingTmp, 0o700); err != nil {
		return "", fmt.Errorf("create runtime temp dir: %w", err)
	}

	// Trusted host preparation: invoke the manifest-verified binary with
	// --version and no repository input so Nuitka unpacks its static runtime.
	if err := runRuntimePreparation(ctx, bin, stagingDir, stagingCache, stagingTmp); err != nil {
		return "", err
	}
	if err := os.RemoveAll(stagingTmp); err != nil {
		return "", fmt.Errorf("remove runtime staging temp: %w", err)
	}

	entries, err := collectRuntimeEntries(stagingCache)
	if err != nil {
		return "", fmt.Errorf("inventory prepared runtime: %w", err)
	}
	if len(entries) == 0 {
		// Non-onefile binary (e.g. a plain executable or test fixture) unpacked
		// nothing into XDG_CACHE_HOME; no separate read-only runtime is needed.
		return "", nil
	}

	manifest := opengrepRuntimeManifest{
		Version:      opengrepRuntimeFormatVersion,
		BinarySHA256: binarySHA256,
		Files:        entries,
	}
	rawManifest, err := json.Marshal(manifest)
	if err != nil {
		return "", fmt.Errorf("encode runtime manifest: %w", err)
	}
	manifestPath := filepath.Join(stagingDir, opengrepRuntimeManifestName)
	if err := os.WriteFile(manifestPath, rawManifest, 0o444); err != nil {
		return "", fmt.Errorf("write runtime manifest: %w", err)
	}
	if err := sealTreeReadOnly(stagingCache); err != nil {
		return "", fmt.Errorf("seal runtime read-only: %w", err)
	}
	if err := os.Chmod(manifestPath, 0o444); err != nil {
		return "", fmt.Errorf("seal runtime manifest: %w", err)
	}
	if err := os.Rename(stagingDir, targetDir); err != nil {
		return "", fmt.Errorf("publish prepared runtime: %w", err)
	}
	published = true
	if err := os.Chmod(targetDir, 0o555); err != nil {
		return "", fmt.Errorf("seal published runtime root: %w", err)
	}
	if err := verifyPreparedRuntime(targetDir, binarySHA256); err != nil {
		_ = removeReadOnlyTree(targetDir)
		return "", fmt.Errorf("verify published runtime: %w", err)
	}
	pruneStaleRuntimeDirs(storeRoot, filepath.Base(targetDir))
	return filepath.Join(targetDir, opengrepRuntimeCacheSubdir), nil
}

func runRuntimePreparation(ctx context.Context, bin, workDir, cacheDir, tmpDir string) error {
	env := opengrepPreparationEnv(cacheDir, tmpDir)
	stdout, stderr, exitCode, err := exec.RunSeparate(ctx, bin, []string{"--version"}, exec.ExecOpts{
		Launch:         exec.HostLaunch("opengrep-runtime-prepare"),
		Dir:            workDir,
		Timeout:        opengrepRuntimePrepTimeout,
		MaxOutputBytes: 64 << 10,
		Env:            env,
	})
	if err != nil {
		detail := strings.TrimSpace(string(append(stdout, stderr...)))
		if detail != "" {
			return fmt.Errorf("prepare opengrep runtime (exit %d): %w (%s)", exitCode, err, detail)
		}
		return fmt.Errorf("prepare opengrep runtime (exit %d): %w", exitCode, err)
	}
	return nil
}

func opengrepPreparationEnv(cacheDir, tmpDir string) []string {
	return overrideEnv(os.Environ(), map[string]string{
		"XDG_CACHE_HOME":             cacheDir,
		"TMPDIR":                     tmpDir,
		"TMP":                        tmpDir,
		"TEMP":                       tmpDir,
		"LANG":                       "C.UTF-8",
		"LC_ALL":                     "C.UTF-8",
		"LC_CTYPE":                   "C.UTF-8",
		"PYTHONIOENCODING":           "utf-8",
		"PYTHONUTF8":                 "1",
		"SEMGREP_LOG_FILE":           filepath.Join(tmpDir, "prep.log"),
		"SEMGREP_SETTINGS_FILE":      filepath.Join(tmpDir, "settings.yml"),
		"SEMGREP_VERSION_CACHE_PATH": filepath.Join(tmpDir, "version-cache"),
		"SEMGREP_SEND_METRICS":       "off",
	})
}

func overrideEnv(base []string, pins map[string]string) []string {
	out := append([]string(nil), base...)
	for k, v := range pins {
		prefix := k + "="
		replaced := false
		for i, kv := range out {
			if strings.HasPrefix(kv, prefix) {
				out[i] = prefix + v
				replaced = true
				break
			}
		}
		if !replaced {
			out = append(out, prefix+v)
		}
	}
	return out
}

func collectRuntimeEntries(cacheDir string) ([]opengrepRuntimeFileEntry, error) {
	var entries []opengrepRuntimeFileEntry
	err := filepath.WalkDir(cacheDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == cacheDir {
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("unexpected symlink in prepared runtime: %s", path)
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("unexpected non-regular entry in prepared runtime: %s", path)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(cacheDir, path)
		if err != nil {
			return err
		}
		digest, err := sha256RegularFile(path)
		if err != nil {
			return err
		}
		entries = append(entries, opengrepRuntimeFileEntry{
			RelPath:    filepath.ToSlash(rel),
			Size:       info.Size(),
			Executable: info.Mode().Perm()&0o111 != 0,
			SHA256:     digest,
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].RelPath < entries[j].RelPath
	})
	return entries, nil
}

func verifyPreparedRuntime(targetDir, expectedBinarySHA256 string) error {
	rootInfo, err := os.Lstat(targetDir)
	if err != nil {
		return err
	}
	if !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("runtime root is not a real directory")
	}
	if runtime.GOOS != "windows" && rootInfo.Mode().Perm()&0o222 != 0 {
		return fmt.Errorf("runtime root is writable (%o)", rootInfo.Mode().Perm())
	}

	manifestPath := filepath.Join(targetDir, opengrepRuntimeManifestName)
	manifestInfo, err := os.Lstat(manifestPath)
	if err != nil {
		return err
	}
	if !manifestInfo.Mode().IsRegular() || manifestInfo.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("runtime manifest is not a regular file")
	}
	if runtime.GOOS != "windows" && manifestInfo.Mode().Perm()&0o222 != 0 {
		return fmt.Errorf("runtime manifest is writable (%o)", manifestInfo.Mode().Perm())
	}
	raw, err := os.ReadFile(manifestPath) // #nosec G304 -- validated host-owned runtime manifest
	if err != nil {
		return err
	}
	var manifest opengrepRuntimeManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return fmt.Errorf("parse runtime manifest: %w", err)
	}
	if manifest.Version != opengrepRuntimeFormatVersion || manifest.BinarySHA256 != expectedBinarySHA256 || len(manifest.Files) == 0 {
		return fmt.Errorf("runtime manifest provenance mismatch")
	}

	expected := make(map[string]opengrepRuntimeFileEntry, len(manifest.Files))
	for _, entry := range manifest.Files {
		if entry.RelPath == "" || !filepath.IsLocal(filepath.FromSlash(entry.RelPath)) {
			return fmt.Errorf("invalid runtime manifest path %q", entry.RelPath)
		}
		if _, dup := expected[entry.RelPath]; dup {
			return fmt.Errorf("duplicate runtime manifest path %q", entry.RelPath)
		}
		expected[entry.RelPath] = entry
	}

	cacheDir := filepath.Join(targetDir, opengrepRuntimeCacheSubdir)
	seen := 0
	err = filepath.WalkDir(cacheDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("unexpected symlink in runtime: %s", path)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if runtime.GOOS != "windows" && info.Mode().Perm()&0o222 != 0 {
			return fmt.Errorf("runtime entry is writable (%o): %s", info.Mode().Perm(), path)
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("unexpected non-regular runtime entry: %s", path)
		}
		rel, err := filepath.Rel(cacheDir, path)
		if err != nil {
			return err
		}
		key := filepath.ToSlash(rel)
		want, ok := expected[key]
		if !ok {
			return fmt.Errorf("unexpected file in prepared runtime: %s", key)
		}
		if info.Size() != want.Size {
			return fmt.Errorf("runtime file size mismatch for %s: got %d want %d", key, info.Size(), want.Size)
		}
		digest, err := sha256RegularFile(path)
		if err != nil {
			return err
		}
		if digest != want.SHA256 {
			return fmt.Errorf("runtime file digest mismatch for %s", key)
		}
		seen++
		return nil
	})
	if err != nil {
		return err
	}
	if seen != len(expected) {
		return fmt.Errorf("runtime missing %d of %d expected files", len(expected)-seen, len(expected))
	}
	return nil
}

func sealTreeReadOnly(root string) error {
	var dirs []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			dirs = append(dirs, path)
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		mode := os.FileMode(0o444)
		if info.Mode().Perm()&0o111 != 0 {
			mode = 0o555
		}
		return os.Chmod(path, mode)
	})
	if err != nil {
		return err
	}
	// Seal directories deepest-first so chmod succeeds on all filesystems.
	for i := len(dirs) - 1; i >= 0; i-- {
		if err := os.Chmod(dirs[i], 0o555); err != nil {
			return err
		}
	}
	return nil
}

func removeReadOnlyTree(root string) error {
	if _, err := os.Lstat(root); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if d.IsDir() {
			_ = os.Chmod(path, 0o700)
		} else {
			_ = os.Chmod(path, 0o600)
		}
		return nil
	})
	return os.RemoveAll(root)
}

func pruneStaleRuntimeDirs(storeRoot, keepBase string) {
	entries, err := os.ReadDir(storeRoot)
	if err != nil {
		return
	}
	for _, entry := range entries {
		name := entry.Name()
		if name == keepBase || name == opengrepRuntimeLockName {
			continue
		}
		if strings.HasPrefix(name, opengrepRuntimeDirPrefix) || strings.HasPrefix(name, opengrepRuntimeStagePrefix) {
			_ = removeReadOnlyTree(filepath.Join(storeRoot, name))
		}
	}
}

func acquireRuntimeLock(ctx context.Context, lockPath string, exclusive bool) (func(), error) {
	f, err := filelock.Open(lockPath)
	if err != nil {
		return nil, fmt.Errorf("open runtime lock: %w", err)
	}
	for {
		var ok bool
		if exclusive {
			ok, err = filelock.TryExclusive(f)
		} else {
			ok, err = filelock.TryShared(f)
		}
		if err != nil {
			_ = f.Close()
			return nil, fmt.Errorf("lock runtime store: %w", err)
		}
		if ok {
			return func() {
				_ = filelock.Unlock(f)
				_ = f.Close()
			}, nil
		}
		select {
		case <-ctx.Done():
			_ = f.Close()
			return nil, ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
}

func sha256RegularFile(path string) (string, error) {
	st, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !st.Mode().IsRegular() || st.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("not a regular file: %s", path)
	}
	f, err := os.Open(path) // #nosec G304 -- caller-validated file path
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
