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
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/filelock"
	"github.com/lycaon/lycaon/internal/scan"
)

const (
	opengrepRuntimeFormatVersion = 1
	opengrepRuntimeDirPrefix     = "v1-"
	opengrepRuntimeStagePrefix   = ".stage-"
	opengrepRuntimeLockName      = ".prepare.lock"
	opengrepRuntimeLeaseSuffix   = ".lease"
	opengrepRuntimeManifestName  = "manifest.json"
	opengrepRuntimeCacheSubdir   = "xdg-cache"
	opengrepRuntimeMaxDiagBytes  = 512
)

var (
	opengrepRuntimeMu  sync.RWMutex
	verifiedRuntimesMu sync.RWMutex
	verifiedRuntimes   = map[string]verifiedRuntimeRecord{}
	// runtimePrepHook allows tests to observe real confined preparation calls
	// without weakening confinement.
	runtimePrepHook func(bin string)
)

type verifiedRuntimeRecord struct {
	binarySHA256 string
	manifestSize int64
	manifestMod  int64
}

type opengrepRuntimeManifest struct {
	Version      int                        `json:"version"`
	BinarySHA256 string                     `json:"binary_sha256"`
	NoExtract    bool                       `json:"no_extract,omitempty"`
	Files        []opengrepRuntimeFileEntry `json:"files,omitempty"`
}

type opengrepRuntimeFileEntry struct {
	RelPath    string `json:"rel_path"`
	Size       int64  `json:"size"`
	ModTimeNS  int64  `json:"mod_time_ns"`
	Executable bool   `json:"executable,omitempty"`
	SHA256     string `json:"sha256"`
}

func noopRelease() {}

// prepareOpengrepRuntime unpacks (under confinement, with no repository input)
// and seals the verified bundled OpenGrep onefile runtime into the host-owned,
// read-only runtime store under the user config tree. It returns a read-only
// XDG_CACHE_HOME path (empty for plain executables that extract nothing) and a
// lease release function that must be called after the scan finishes so stale
// version pruning never removes a runtime while an overlapping scan uses it.
func prepareOpengrepRuntime(ctx context.Context, bin, verifiedSHA256 string) (string, func(), error) {
	cfgDir, err := configdir.UserConfigDir()
	if err != nil {
		return "", nil, fmt.Errorf("resolve runtime store root: %w", err)
	}
	return prepareOpengrepRuntimeInStore(ctx, bin, verifiedSHA256, enginepaths.OpengrepRuntimeRootUnder(cfgDir))
}

func prepareOpengrepRuntimeInStore(ctx context.Context, bin, verifiedSHA256, storeRoot string) (string, func(), error) {
	binarySHA256 := strings.TrimSpace(verifiedSHA256)
	if binarySHA256 == "" {
		var err error
		binarySHA256, err = sha256RegularFile(bin)
		if err != nil {
			return "", nil, fmt.Errorf("hash bundled binary: %w", err)
		}
	}
	if err := os.MkdirAll(storeRoot, 0o700); err != nil {
		return "", nil, fmt.Errorf("create runtime store: %w", err)
	}
	targetBase := opengrepRuntimeDirPrefix + binarySHA256
	targetDir := filepath.Join(storeRoot, targetBase)
	lockPath := filepath.Join(storeRoot, opengrepRuntimeLockName)
	leasePath := filepath.Join(storeRoot, targetBase+opengrepRuntimeLeaseSuffix)

	if cacheDir, release, ok := tryReusePreparedRuntime(ctx, targetDir, binarySHA256, lockPath, leasePath); ok {
		return cacheDir, release, nil
	}

	opengrepRuntimeMu.Lock()
	defer opengrepRuntimeMu.Unlock()

	releasePrep, err := acquireRuntimeLock(ctx, lockPath, true)
	if err != nil {
		return "", nil, err
	}
	defer releasePrep()

	if manifest, err := verifyPreparedRuntime(targetDir, binarySHA256, false); err == nil {
		return leaseVerifiedRuntime(ctx, targetDir, binarySHA256, leasePath, manifest)
	}
	return buildAndPublishRuntime(ctx, bin, binarySHA256, storeRoot, targetDir, leasePath)
}

func tryReusePreparedRuntime(ctx context.Context, targetDir, binarySHA256, lockPath, leasePath string) (string, func(), bool) {
	opengrepRuntimeMu.RLock()
	defer opengrepRuntimeMu.RUnlock()

	releasePrep, err := acquireRuntimeLock(ctx, lockPath, false)
	if err != nil {
		return "", nil, false
	}
	defer releasePrep()

	fastAllowed := isVerifiedInProcess(targetDir, binarySHA256)
	manifest, err := verifyPreparedRuntime(targetDir, binarySHA256, fastAllowed)
	if err != nil {
		forgetVerifiedInProcess(targetDir)
		return "", nil, false
	}
	cacheDir, releaseLease, err := leaseVerifiedRuntime(ctx, targetDir, binarySHA256, leasePath, manifest)
	if err != nil {
		return "", nil, false
	}
	return cacheDir, releaseLease, true
}

func leaseVerifiedRuntime(ctx context.Context, targetDir, binarySHA256, leasePath string, manifest opengrepRuntimeManifest) (string, func(), error) {
	recordVerifiedInProcess(targetDir, binarySHA256)
	if manifest.NoExtract {
		return "", noopRelease, nil
	}
	releaseLease, err := acquireRuntimeLock(ctx, leasePath, false)
	if err != nil {
		return "", nil, fmt.Errorf("acquire runtime lease: %w", err)
	}
	return filepath.Join(targetDir, opengrepRuntimeCacheSubdir), releaseLease, nil
}

func buildAndPublishRuntime(ctx context.Context, bin, binarySHA256, storeRoot, targetDir, leasePath string) (string, func(), error) {
	// Wait for exclusive lease on targetDir before replacing a damaged entry.
	releaseTargetLease, err := acquireRuntimeLock(ctx, leasePath, true)
	if err != nil {
		return "", nil, fmt.Errorf("lock runtime replacement: %w", err)
	}
	if err := removeReadOnlyTree(targetDir); err != nil {
		releaseTargetLease()
		return "", nil, fmt.Errorf("clear stale runtime: %w", err)
	}
	forgetVerifiedInProcess(targetDir)

	stagingDir, err := os.MkdirTemp(storeRoot, opengrepRuntimeStagePrefix+binarySHA256[:min(12, len(binarySHA256))]+"-")
	if err != nil {
		releaseTargetLease()
		return "", nil, fmt.Errorf("create runtime staging dir: %w", err)
	}
	published := false
	defer func() {
		if !published {
			_ = removeReadOnlyTree(stagingDir)
		}
	}()

	// Run --version inside a confined ActionWorkspace (no egress, no project
	// access, only the ephemeral prep workspace writable) so qualification and
	// scanning both retain applied command confinement.
	unpackedDir, cleanupPrep, err := runConfinedRuntimePreparation(ctx, bin)
	if err != nil {
		releaseTargetLease()
		return "", nil, err
	}
	defer cleanupPrep()

	stagingCache := filepath.Join(stagingDir, opengrepRuntimeCacheSubdir)
	if err := moveOrCopyDir(unpackedDir, stagingCache); err != nil {
		releaseTargetLease()
		return "", nil, fmt.Errorf("stage prepared runtime: %w", err)
	}

	entries, err := collectRuntimeEntries(stagingCache)
	if err != nil {
		releaseTargetLease()
		return "", nil, fmt.Errorf("inventory prepared runtime: %w", err)
	}

	manifest := opengrepRuntimeManifest{
		Version:      opengrepRuntimeFormatVersion,
		BinarySHA256: binarySHA256,
		NoExtract:    len(entries) == 0,
		Files:        entries,
	}
	if err := sealTreeReadOnly(stagingCache); err != nil {
		releaseTargetLease()
		return "", nil, fmt.Errorf("seal runtime read-only: %w", err)
	}
	rawManifest, err := json.Marshal(manifest)
	if err != nil {
		releaseTargetLease()
		return "", nil, fmt.Errorf("encode runtime manifest: %w", err)
	}
	manifestPath := filepath.Join(stagingDir, opengrepRuntimeManifestName)
	if err := os.WriteFile(manifestPath, rawManifest, 0o444); err != nil {
		releaseTargetLease()
		return "", nil, fmt.Errorf("write runtime manifest: %w", err)
	}
	if err := os.Chmod(manifestPath, 0o444); err != nil {
		releaseTargetLease()
		return "", nil, fmt.Errorf("seal runtime manifest: %w", err)
	}
	if err := os.Rename(stagingDir, targetDir); err != nil {
		releaseTargetLease()
		return "", nil, fmt.Errorf("publish prepared runtime: %w", err)
	}
	published = true
	if err := os.Chmod(targetDir, 0o555); err != nil {
		releaseTargetLease()
		return "", nil, fmt.Errorf("seal published runtime root: %w", err)
	}
	releaseTargetLease()

	verified, err := verifyPreparedRuntime(targetDir, binarySHA256, false)
	if err != nil {
		_ = removeReadOnlyTree(targetDir)
		return "", nil, fmt.Errorf("verify published runtime: %w", err)
	}
	cacheDir, releaseLease, err := leaseVerifiedRuntime(ctx, targetDir, binarySHA256, leasePath, verified)
	if err != nil {
		return "", nil, err
	}
	pruneStaleRuntimeDirs(storeRoot, filepath.Base(targetDir))
	return cacheDir, releaseLease, nil
}

func runConfinedRuntimePreparation(ctx context.Context, bin string) (string, func(), error) {
	if runtimePrepHook != nil {
		runtimePrepHook(bin)
	}
	prepWorkspace, err := confine.NewActionWorkspace()
	if err != nil {
		return "", nil, fmt.Errorf("create runtime prep workspace: %w", err)
	}
	cleanup := exec.TrackScratchDir(prepWorkspace.Path())
	ok := false
	defer func() {
		if !ok {
			cleanup()
		}
	}()

	prepCache := filepath.Join(prepWorkspace.Path(), opengrepRuntimeCacheSubdir)
	prepTmp := filepath.Join(prepWorkspace.Path(), "tmp")
	if err := os.MkdirAll(prepCache, 0o700); err != nil {
		return "", nil, fmt.Errorf("create runtime prep cache: %w", err)
	}
	if err := os.MkdirAll(prepTmp, 0o700); err != nil {
		return "", nil, fmt.Errorf("create runtime prep temp: %w", err)
	}

	confinement, err := scan.BundledScannerConfinement(filepath.Dir(bin), prepWorkspace)
	if err != nil {
		return "", nil, fmt.Errorf("confine runtime prep: %w", err)
	}
	stdout, stderr, exitCode, err := exec.RunSeparate(ctx, bin, []string{"--version"}, exec.ExecOpts{
		Launch:         exec.BundledScannerLaunch("opengrep", confinement),
		Dir:            prepWorkspace.Path(),
		NoTimeout:      true,
		MaxOutputBytes: 64 << 10,
		Env:            opengrepPreparationEnv(prepCache, prepTmp),
	})
	if ctx.Err() != nil {
		return "", nil, fmt.Errorf("prepare opengrep runtime interrupted: %w", ctx.Err())
	}
	if err != nil || exitCode != 0 {
		detail := boundedRuntimePrepDiagnostic(stdout, stderr)
		if err != nil {
			if detail != "" {
				return "", nil, fmt.Errorf("prepare opengrep runtime (exit %d): %w (%s)", exitCode, err, detail)
			}
			return "", nil, fmt.Errorf("prepare opengrep runtime (exit %d): %w", exitCode, err)
		}
		if detail != "" {
			return "", nil, fmt.Errorf("prepare opengrep runtime failed with exit %d (%s)", exitCode, detail)
		}
		return "", nil, fmt.Errorf("prepare opengrep runtime failed with exit %d", exitCode)
	}
	ok = true
	return prepCache, cleanup, nil
}

func boundedRuntimePrepDiagnostic(stdout, stderr []byte) string {
	combined := strings.TrimSpace(string(append(append([]byte(nil), stdout...), stderr...)))
	if len(combined) > opengrepRuntimeMaxDiagBytes {
		combined = combined[len(combined)-opengrepRuntimeMaxDiagBytes:]
	}
	return combined
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

func moveOrCopyDir(src, dst string) error {
	srcInfo, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if !srcInfo.IsDir() || srcInfo.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("prepared runtime source is not a real directory: %s", src)
	}
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("unexpected symlink in prepared runtime: %s", path)
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := dst
		if rel != "." {
			target = filepath.Join(dst, rel)
		}
		if d.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("unexpected non-regular entry in prepared runtime: %s", path)
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("unexpected non-regular entry in prepared runtime: %s", path)
		}
		if err := checkSingleLink(path, info); err != nil {
			return err
		}
		mode := os.FileMode(0o600)
		if info.Mode().Perm()&0o111 != 0 {
			mode = 0o700
		}
		in, err := os.Open(path) // #nosec G304 -- validated staging file
		if err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode) // #nosec G304 -- validated target path
		if err != nil {
			_ = in.Close()
			return err
		}
		_, copyErr := io.Copy(out, in)
		closeOutErr := out.Close()
		closeInErr := in.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeOutErr != nil {
			return closeOutErr
		}
		return closeInErr
	})
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
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("unexpected non-regular entry in prepared runtime: %s", path)
		}
		if err := checkSingleLink(path, info); err != nil {
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
			ModTimeNS:  info.ModTime().UnixNano(),
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

// verifyPreparedRuntime validates the sealed runtime root, manifest provenance,
// and directory tree. When metadataOnly is true (warm reuse after this process
// has already verified the cryptographic digests), it verifies permissions,
// structure, sizes, and mtimes without re-reading hundreds of MiB of payload.
func verifyPreparedRuntime(targetDir, expectedBinarySHA256 string, metadataOnly bool) (opengrepRuntimeManifest, error) {
	var empty opengrepRuntimeManifest
	rootInfo, err := os.Lstat(targetDir)
	if err != nil {
		return empty, err
	}
	if !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return empty, fmt.Errorf("runtime root is not a real directory")
	}
	if runtime.GOOS != "windows" && rootInfo.Mode().Perm()&0o222 != 0 {
		return empty, fmt.Errorf("runtime root is writable (%o)", rootInfo.Mode().Perm())
	}

	manifestPath := filepath.Join(targetDir, opengrepRuntimeManifestName)
	manifestInfo, err := os.Lstat(manifestPath)
	if err != nil {
		return empty, err
	}
	if !manifestInfo.Mode().IsRegular() || manifestInfo.Mode()&os.ModeSymlink != 0 {
		return empty, fmt.Errorf("runtime manifest is not a regular file")
	}
	if runtime.GOOS != "windows" && manifestInfo.Mode().Perm()&0o222 != 0 {
		return empty, fmt.Errorf("runtime manifest is writable (%o)", manifestInfo.Mode().Perm())
	}
	raw, err := os.ReadFile(manifestPath) // #nosec G304 -- validated host-owned runtime manifest
	if err != nil {
		return empty, err
	}
	var manifest opengrepRuntimeManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return empty, fmt.Errorf("parse runtime manifest: %w", err)
	}
	if manifest.Version != opengrepRuntimeFormatVersion || manifest.BinarySHA256 != expectedBinarySHA256 {
		return empty, fmt.Errorf("runtime manifest provenance mismatch")
	}
	if manifest.NoExtract {
		if len(manifest.Files) != 0 {
			return empty, fmt.Errorf("no-extract runtime manifest unexpectedly lists files")
		}
		return manifest, nil
	}
	if len(manifest.Files) == 0 {
		return empty, fmt.Errorf("runtime manifest lists no files")
	}

	expected := make(map[string]opengrepRuntimeFileEntry, len(manifest.Files))
	for _, entry := range manifest.Files {
		if entry.RelPath == "" || !filepath.IsLocal(filepath.FromSlash(entry.RelPath)) {
			return empty, fmt.Errorf("invalid runtime manifest path %q", entry.RelPath)
		}
		if _, dup := expected[entry.RelPath]; dup {
			return empty, fmt.Errorf("duplicate runtime manifest path %q", entry.RelPath)
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
		if err := checkSingleLink(path, info); err != nil {
			return err
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
		if metadataOnly {
			if info.ModTime().UnixNano() != want.ModTimeNS {
				return fmt.Errorf("runtime file mtime mismatch for %s", key)
			}
		} else {
			digest, err := sha256RegularFile(path)
			if err != nil {
				return err
			}
			if digest != want.SHA256 {
				return fmt.Errorf("runtime file digest mismatch for %s", key)
			}
		}
		seen++
		return nil
	})
	if err != nil {
		return empty, err
	}
	if seen != len(expected) {
		return empty, fmt.Errorf("runtime missing %d of %d expected files", len(expected)-seen, len(expected))
	}
	return manifest, nil
}

func isVerifiedInProcess(targetDir, binarySHA256 string) bool {
	verifiedRuntimesMu.RLock()
	rec, ok := verifiedRuntimes[targetDir]
	verifiedRuntimesMu.RUnlock()
	if !ok || rec.binarySHA256 != binarySHA256 {
		return false
	}
	info, err := os.Lstat(filepath.Join(targetDir, opengrepRuntimeManifestName))
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	return info.Size() == rec.manifestSize && info.ModTime().UnixNano() == rec.manifestMod
}

func recordVerifiedInProcess(targetDir, binarySHA256 string) {
	info, err := os.Lstat(filepath.Join(targetDir, opengrepRuntimeManifestName))
	if err != nil {
		return
	}
	verifiedRuntimesMu.Lock()
	verifiedRuntimes[targetDir] = verifiedRuntimeRecord{
		binarySHA256: binarySHA256,
		manifestSize: info.Size(),
		manifestMod:  info.ModTime().UnixNano(),
	}
	verifiedRuntimesMu.Unlock()
}

func forgetVerifiedInProcess(targetDir string) {
	verifiedRuntimesMu.Lock()
	delete(verifiedRuntimes, targetDir)
	verifiedRuntimesMu.Unlock()
}

func sealTreeReadOnly(root string) error {
	rootInfo, err := os.Lstat(root)
	if err != nil {
		return err
	}
	if !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("cannot seal non-directory runtime root: %s", root)
	}
	var dirs []string
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("cannot seal symlink in runtime tree: %s", path)
		}
		if d.IsDir() {
			dirs = append(dirs, path)
			return nil
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("cannot seal non-regular entry in runtime tree: %s", path)
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("cannot seal non-regular entry in runtime tree: %s", path)
		}
		if err := checkSingleLink(path, info); err != nil {
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
	info, err := os.Lstat(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	// Never follow or chmod a symlink at root; unlink it directly.
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return os.Remove(root)
	}
	// Unlinking regular files and symlinks only requires write+exec on their
	// parent directories. Never chmod non-directories or symlinks so cleanup
	// cannot alter permissions on symlink targets or hard-linked external files.
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if d.IsDir() {
			_ = os.Chmod(path, 0o700)
		}
		return nil
	})
	return os.RemoveAll(root)
}

// pruneStaleRuntimeDirs removes older runtime directories only when no active
// scanner holds a shared lease on that version.
func pruneStaleRuntimeDirs(storeRoot, keepBase string) {
	entries, err := os.ReadDir(storeRoot)
	if err != nil {
		return
	}
	keepLease := keepBase + opengrepRuntimeLeaseSuffix
	for _, entry := range entries {
		name := entry.Name()
		if name == keepBase || name == keepLease || name == opengrepRuntimeLockName {
			continue
		}
		if strings.HasPrefix(name, opengrepRuntimeStagePrefix) {
			_ = removeReadOnlyTree(filepath.Join(storeRoot, name))
			continue
		}
		if !strings.HasPrefix(name, opengrepRuntimeDirPrefix) || strings.HasSuffix(name, opengrepRuntimeLeaseSuffix) {
			continue
		}
		leasePath := filepath.Join(storeRoot, name+opengrepRuntimeLeaseSuffix)
		f, err := filelock.Open(leasePath)
		if err != nil {
			continue
		}
		locked, err := filelock.TryExclusive(f)
		if err != nil || !locked {
			_ = f.Close()
			continue
		}
		target := filepath.Join(storeRoot, name)
		_ = removeReadOnlyTree(target)
		forgetVerifiedInProcess(target)
		_ = filelock.Unlock(f)
		_ = f.Close()
		_ = os.Remove(leasePath)
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
