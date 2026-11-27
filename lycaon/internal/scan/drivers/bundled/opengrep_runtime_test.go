package bundleddriver

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/confine/admissiontest"
	"github.com/lycaon/lycaon/internal/testutil"
)

func installPrepCounter(t *testing.T) *atomic.Int32 {
	t.Helper()
	var count atomic.Int32
	prev := runtimePrepHook
	runtimePrepHook = func(string) { count.Add(1) }
	t.Cleanup(func() { runtimePrepHook = prev })
	return &count
}

func writeFakeOnefileBinary(t *testing.T, dir, name, version string) string {
	t.Helper()
	script := fmt.Sprintf(`#!/bin/sh
set -e
target="$XDG_CACHE_HOME/opengrep/paintedwolf-%s"
mkdir -p "$target/semgrep/bin"
printf 'engine-%s' > "$target/semgrep/bin/opengrep-core"
chmod 755 "$target/semgrep/bin/opengrep-core"
printf 'schema-%s' > "$target/schema.yaml"
chmod 644 "$target/schema.yaml"
printf '%s\n'
`, version, version, version, version)
	bin := filepath.Join(dir, name)
	testutil.FailErr(t, "write fake onefile binary", os.WriteFile(bin, []byte(script), 0o700))
	return bin
}

func writeFakePlainBinary(t *testing.T, dir, name, version string) string {
	t.Helper()
	script := fmt.Sprintf("#!/bin/sh\nprintf '%s\\n'\n", version)
	bin := filepath.Join(dir, name)
	testutil.FailErr(t, "write fake plain binary", os.WriteFile(bin, []byte(script), 0o700))
	return bin
}

func TestPrepareOpengrepRuntimeColdWarmAndConcurrent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell onefile fixture requires macOS or Linux")
	}
	admissiontest.Enable(t)
	invocations := installPrepCounter(t)
	root := t.TempDir()
	store := filepath.Join(root, "runtime-store")
	bin := writeFakeOnefileBinary(t, root, "opengrep", "1.30.0")
	t.Cleanup(func() { _ = removeReadOnlyTree(store) })

	const workers = 6
	results := make([]string, workers)
	releases := make([]func(), workers)
	errs := make([]error, workers)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range workers {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-start
			results[idx], releases[idx], errs[idx] = prepareOpengrepRuntimeInStore(t.Context(), bin, "", store)
		}(i)
	}
	close(start)
	wg.Wait()

	t.Cleanup(func() {
		for _, release := range releases {
			if release != nil {
				release()
			}
		}
	})
	for i := range workers {
		testutil.FailErr(t, fmt.Sprintf("concurrent prepare %d", i), errs[i])
		if results[i] == "" || results[i] != results[0] {
			t.Fatalf("worker %d got %q, want %q", i, results[i], results[0])
		}
	}
	if got := invocations.Load(); got != 1 {
		t.Fatalf("cold concurrent preparation invoked binary %d times, want 1", got)
	}

	// Warm reuse must hit the verified read-only store without re-invoking bin.
	warm, releaseWarm, err := prepareOpengrepRuntimeInStore(t.Context(), bin, "", store)
	testutil.FailErr(t, "warm prepare", err)
	defer releaseWarm()
	if warm != results[0] {
		t.Fatalf("warm runtime = %q, want %q", warm, results[0])
	}
	if got := invocations.Load(); got != 1 {
		t.Fatalf("warm reuse invoked binary %d times, want 1", got)
	}

	// Every directory and file in the published runtime must be strictly read-only.
	targetDir := filepath.Dir(warm)
	err = filepath.WalkDir(targetDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.Mode().Perm()&0o222 != 0 {
			t.Errorf("prepared runtime entry is writable (%o): %s", info.Mode().Perm(), path)
		}
		return nil
	})
	testutil.FailErr(t, "walk sealed runtime", err)
}

func TestPrepareOpengrepRuntimeDetectsTamperingAndRebuilds(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell onefile fixture requires macOS or Linux")
	}
	admissiontest.Enable(t)
	invocations := installPrepCounter(t)
	root := t.TempDir()
	store := filepath.Join(root, "runtime-store")
	bin := writeFakeOnefileBinary(t, root, "opengrep", "1.30.0")
	t.Cleanup(func() { _ = removeReadOnlyTree(store) })

	cacheDir, release1, err := prepareOpengrepRuntimeInStore(t.Context(), bin, "", store)
	testutil.FailErr(t, "initial prepare", err)
	release1()
	if got := invocations.Load(); got != 1 {
		t.Fatalf("initial invocations = %d, want 1", got)
	}

	// Tamper with a cached executable byte and re-seal it; simulating a fresh
	// process (full SHA-256 verification) must reject reuse and rebuild cleanly.
	corePath := filepath.Join(cacheDir, "opengrep", "paintedwolf-1.30.0", "semgrep", "bin", "opengrep-core")
	testutil.FailErr(t, "unseal core for tamper test", os.Chmod(corePath, 0o600))
	testutil.FailErr(t, "tamper cached engine", os.WriteFile(corePath, []byte("tampered"), 0o600))
	testutil.FailErr(t, "reseal tampered engine", os.Chmod(corePath, 0o555))
	forgetVerifiedInProcess(filepath.Dir(cacheDir))

	rebuilt, release2, err := prepareOpengrepRuntimeInStore(t.Context(), bin, "", store)
	testutil.FailErr(t, "rebuild after tamper", err)
	release2()
	if rebuilt != cacheDir {
		t.Fatalf("rebuilt cacheDir = %q, want %q", rebuilt, cacheDir)
	}
	if got := invocations.Load(); got != 2 {
		t.Fatalf("invocations after tamper = %d, want 2", got)
	}
	restored, err := os.ReadFile(corePath)
	testutil.FailErr(t, "read restored core", err)
	if string(restored) != "engine-1.30.0" {
		t.Fatalf("restored engine bytes = %q, want %q", restored, "engine-1.30.0")
	}

	// An unexpected extra file inside the cache must also force a clean rebuild
	// even on the fast in-process metadata path.
	unpackedDir := filepath.Join(cacheDir, "opengrep", "paintedwolf-1.30.0")
	testutil.FailErr(t, "unseal unpacked dir", os.Chmod(unpackedDir, 0o700))
	extraPath := filepath.Join(unpackedDir, "injected.so")
	testutil.FailErr(t, "inject extra file", os.WriteFile(extraPath, []byte("evil"), 0o444))
	testutil.FailErr(t, "reseal unpacked dir", os.Chmod(unpackedDir, 0o555))

	_, release3, err := prepareOpengrepRuntimeInStore(t.Context(), bin, "", store)
	testutil.FailErr(t, "rebuild after injection", err)
	release3()
	if got := invocations.Load(); got != 3 {
		t.Fatalf("invocations after injection = %d, want 3", got)
	}
	if _, err := os.Stat(extraPath); !os.IsNotExist(err) {
		t.Fatalf("injected file survived rebuild: err=%v", err)
	}
}

func TestPrepareOpengrepRuntimeLeaseProtectsOverlappingVersion(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell onefile fixture requires macOS or Linux")
	}
	admissiontest.Enable(t)
	root := t.TempDir()
	store := filepath.Join(root, "runtime-store")
	binV1 := writeFakeOnefileBinary(t, root, "opengrep-v1", "1.30.0")
	binV2 := writeFakeOnefileBinary(t, root, "opengrep-v2", "1.31.0")
	binV3 := writeFakeOnefileBinary(t, root, "opengrep-v3", "1.32.0")
	t.Cleanup(func() { _ = removeReadOnlyTree(store) })

	cacheV1, releaseV1, err := prepareOpengrepRuntimeInStore(t.Context(), binV1, "", store)
	testutil.FailErr(t, "prepare v1", err)
	dirV1 := filepath.Dir(cacheV1)

	// While v1's scan lease is still held, preparing v2 must NOT prune v1.
	cacheV2, releaseV2, err := prepareOpengrepRuntimeInStore(t.Context(), binV2, "", store)
	testutil.FailErr(t, "prepare v2 while v1 leased", err)
	dirV2 := filepath.Dir(cacheV2)
	if _, err := os.Stat(dirV1); err != nil {
		t.Fatalf("actively leased runtime %q was pruned prematurely: %v", dirV1, err)
	}
	if _, err := os.Stat(dirV2); err != nil {
		t.Fatalf("new runtime dir %q missing: %v", dirV2, err)
	}

	// Release v1 and v2 leases; preparing v3 must now prune both unleased versions.
	releaseV1()
	releaseV2()
	cacheV3, releaseV3, err := prepareOpengrepRuntimeInStore(t.Context(), binV3, "", store)
	testutil.FailErr(t, "prepare v3 after leases released", err)
	defer releaseV3()
	dirV3 := filepath.Dir(cacheV3)

	if _, err := os.Stat(dirV1); !os.IsNotExist(err) {
		t.Fatalf("unleased stale runtime %q was not pruned: err=%v", dirV1, err)
	}
	if _, err := os.Stat(dirV2); !os.IsNotExist(err) {
		t.Fatalf("unleased stale runtime %q was not pruned: err=%v", dirV2, err)
	}
	if _, err := os.Stat(dirV3); err != nil {
		t.Fatalf("active runtime dir %q missing: %v", dirV3, err)
	}
}

func TestPrepareOpengrepRuntimePlainBinarySkipsRepeatedProbes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture requires macOS or Linux")
	}
	admissiontest.Enable(t)
	invocations := installPrepCounter(t)
	root := t.TempDir()
	store := filepath.Join(root, "runtime-store")
	bin := writeFakePlainBinary(t, root, "opengrep-plain", "1.30.0")
	t.Cleanup(func() { _ = removeReadOnlyTree(store) })

	for i := range 3 {
		cacheDir, release, err := prepareOpengrepRuntimeInStore(t.Context(), bin, "", store)
		testutil.FailErr(t, fmt.Sprintf("prepare plain binary %d", i), err)
		release()
		if cacheDir != "" {
			t.Fatalf("call %d returned runtime cache %q for plain binary, want empty", i, cacheDir)
		}
	}
	if got := invocations.Load(); got != 1 {
		t.Fatalf("plain binary probed %d times across 3 calls, want 1", got)
	}
}

func TestRemoveReadOnlyTreePreservesExternalSentinels(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission and link semantics require macOS or Linux")
	}
	root := t.TempDir()
	externalDir := filepath.Join(root, "external")
	testutil.FailErr(t, "create external dir", os.MkdirAll(externalDir, 0o700))

	sentinelPath := filepath.Join(externalDir, "sentinel.txt")
	const sentinelBody = "external-sentinel-bytes"
	testutil.FailErr(t, "write sentinel", os.WriteFile(sentinelPath, []byte(sentinelBody), 0o640))
	testutil.FailErr(t, "chmod sentinel", os.Chmod(sentinelPath, 0o640))

	hardTargetPath := filepath.Join(externalDir, "hard-target.txt")
	const hardTargetBody = "external-hardlink-bytes"
	testutil.FailErr(t, "write hard target", os.WriteFile(hardTargetPath, []byte(hardTargetBody), 0o640))
	testutil.FailErr(t, "chmod hard target", os.Chmod(hardTargetPath, 0o640))

	dirSentinelPath := filepath.Join(externalDir, "dir-sentinel.txt")
	testutil.FailErr(t, "write dir sentinel", os.WriteFile(dirSentinelPath, []byte("keep"), 0o640))

	damaged := filepath.Join(root, "damaged-runtime")
	nested := filepath.Join(damaged, "sub")
	testutil.FailErr(t, "create damaged tree", os.MkdirAll(nested, 0o700))
	testutil.FailErr(t, "write read-only file", os.WriteFile(filepath.Join(nested, "ro.bin"), []byte("x"), 0o444))
	testutil.FailErr(t, "symlink to sentinel", os.Symlink(sentinelPath, filepath.Join(nested, "link-sentinel")))
	testutil.FailErr(t, "symlink to external dir", os.Symlink(externalDir, filepath.Join(damaged, "link-external-dir")))
	testutil.FailErr(t, "hardlink to external file", os.Link(hardTargetPath, filepath.Join(nested, "hard-external")))
	testutil.FailErr(t, "seal nested dir", os.Chmod(nested, 0o555))
	testutil.FailErr(t, "seal damaged root", os.Chmod(damaged, 0o555))

	testutil.FailErr(t, "removeReadOnlyTree", removeReadOnlyTree(damaged))

	if _, err := os.Lstat(damaged); !os.IsNotExist(err) {
		t.Fatalf("damaged tree still exists: err=%v", err)
	}
	assertSentinelIntact := func(path, wantBody string, wantPerm os.FileMode) {
		t.Helper()
		st, err := os.Lstat(path)
		testutil.FailErr(t, "stat "+path, err)
		if st.Mode().Perm() != wantPerm {
			t.Fatalf("%s mode = %o, want unchanged %o", path, st.Mode().Perm(), wantPerm)
		}
		got, err := os.ReadFile(path)
		testutil.FailErr(t, "read "+path, err)
		if string(got) != wantBody {
			t.Fatalf("%s body = %q, want %q", path, got, wantBody)
		}
	}
	assertSentinelIntact(sentinelPath, sentinelBody, 0o640)
	assertSentinelIntact(hardTargetPath, hardTargetBody, 0o640)
	assertSentinelIntact(dirSentinelPath, "keep", 0o640)
}

func TestPrepareOpengrepRuntimeRejectsSymlinksAndHardlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission and link semantics require macOS or Linux")
	}
	admissiontest.Enable(t)
	root := t.TempDir()
	store := filepath.Join(root, "runtime-store")

	// 1. Failed preparation containing a symlink to an external sentinel must
	// reject publication, clean up staging, and preserve the sentinel's mode and bytes.
	sentinel := filepath.Join(root, "external-sentinel")
	testutil.FailErr(t, "write sentinel", os.WriteFile(sentinel, []byte("untouched"), 0o640))
	testutil.FailErr(t, "chmod sentinel", os.Chmod(sentinel, 0o640))

	badBin := filepath.Join(root, "opengrep-symlink")
	badScript := fmt.Sprintf("#!/bin/sh\nset -e\nmkdir -p \"$XDG_CACHE_HOME/opengrep\"\nln -s %q \"$XDG_CACHE_HOME/opengrep/escape\"\nprintf '1.30.0\\n'\n", sentinel)
	testutil.FailErr(t, "write bad symlink binary", os.WriteFile(badBin, []byte(badScript), 0o700))
	t.Cleanup(func() { _ = removeReadOnlyTree(store) })

	if _, _, err := prepareOpengrepRuntimeInStore(t.Context(), badBin, "", store); err == nil {
		t.Fatal("preparation unexpectedly accepted a symlink in unpacked runtime")
	}
	st, err := os.Lstat(sentinel)
	testutil.FailErr(t, "stat external sentinel after rejected prep", err)
	if st.Mode().Perm() != 0o640 {
		t.Fatalf("external sentinel mode mutated to %o, want 0640", st.Mode().Perm())
	}
	raw, err := os.ReadFile(sentinel)
	testutil.FailErr(t, "read external sentinel after rejected prep", err)
	if string(raw) != "untouched" {
		t.Fatalf("external sentinel content = %q, want untouched", raw)
	}

	// 2. sealTreeReadOnly must refuse hard-linked files without mutating the external inode.
	hardTree := filepath.Join(root, "hard-tree")
	testutil.FailErr(t, "create hard-tree", os.MkdirAll(hardTree, 0o700))
	testutil.FailErr(t, "hardlink sentinel", os.Link(sentinel, filepath.Join(hardTree, "linked")))
	if err := sealTreeReadOnly(hardTree); err == nil {
		t.Fatal("sealTreeReadOnly unexpectedly sealed a hard-linked file")
	}
	st, err = os.Lstat(sentinel)
	testutil.FailErr(t, "stat sentinel after rejected seal", err)
	if st.Mode().Perm() != 0o640 {
		t.Fatalf("hard-linked external sentinel mode mutated to %o, want 0640", st.Mode().Perm())
	}
}

func TestPrepareOpengrepRuntimeRejectsNonzeroExitAndRecovers(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture requires macOS or Linux")
	}
	admissiontest.Enable(t)
	invocations := installPrepCounter(t)
	root := t.TempDir()
	store := filepath.Join(root, "runtime-store")
	t.Cleanup(func() { _ = removeReadOnlyTree(store) })

	bin := filepath.Join(root, "opengrep-flaky")
	script := `#!/bin/sh
set -e
target="$XDG_CACHE_HOME/opengrep/paintedwolf-1.30.0"
mkdir -p "$target/semgrep/bin"
if [ "${OPENGREP_FAIL_PREP:-}" = "1" ]; then
  printf 'partial' > "$target/partial.bin"
  printf 'partial extraction failed\n' >&2
  exit 7
fi
printf 'engine-1.30.0' > "$target/semgrep/bin/opengrep-core"
chmod 755 "$target/semgrep/bin/opengrep-core"
printf '1.30.0\n'
`
	testutil.FailErr(t, "write flaky binary", os.WriteFile(bin, []byte(script), 0o700))

	// Attempt 1: nonzero exit after partial extraction must fail, preserve
	// bounded diagnostics, publish nothing, and clean up staging.
	t.Setenv("OPENGREP_FAIL_PREP", "1")
	cacheDir, release, err := prepareOpengrepRuntimeInStore(t.Context(), bin, "", store)
	if err == nil || cacheDir != "" || release != nil {
		t.Fatalf("nonzero prep exit unexpectedly succeeded: cacheDir=%q err=%v", cacheDir, err)
	}
	if !strings.Contains(err.Error(), "exit 7") || !strings.Contains(err.Error(), "partial extraction failed") {
		t.Fatalf("prep error lost exit code or diagnostic: %v", err)
	}
	entries, readErr := os.ReadDir(store)
	testutil.FailErr(t, "read store after failed prep", readErr)
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, opengrepRuntimeDirPrefix) && !strings.HasSuffix(name, opengrepRuntimeLeaseSuffix) {
			t.Fatalf("failed prep published runtime directory %q", name)
		}
		if strings.HasPrefix(name, opengrepRuntimeStagePrefix) {
			t.Fatalf("failed prep leaked staging directory %q", name)
		}
	}

	// Attempt 2: retry succeeds, publishes the complete runtime, and leaves no partial file.
	t.Setenv("OPENGREP_FAIL_PREP", "")
	cacheDir, release, err = prepareOpengrepRuntimeInStore(t.Context(), bin, "", store)
	testutil.FailErr(t, "retry prepare after failure", err)
	defer release()
	if cacheDir == "" {
		t.Fatal("retry returned empty runtime cache dir (failed attempt left NoExtract)")
	}
	if got := invocations.Load(); got != 2 {
		t.Fatalf("invocations = %d, want 2", got)
	}
	corePath := filepath.Join(cacheDir, "opengrep", "paintedwolf-1.30.0", "semgrep", "bin", "opengrep-core")
	raw, err := os.ReadFile(corePath)
	testutil.FailErr(t, "read recovered engine", err)
	if string(raw) != "engine-1.30.0" {
		t.Fatalf("recovered engine = %q, want engine-1.30.0", raw)
	}
	if _, err := os.Stat(filepath.Join(cacheDir, "opengrep", "paintedwolf-1.30.0", "partial.bin")); !os.IsNotExist(err) {
		t.Fatalf("partial file from failed attempt survived into published runtime: err=%v", err)
	}
}
