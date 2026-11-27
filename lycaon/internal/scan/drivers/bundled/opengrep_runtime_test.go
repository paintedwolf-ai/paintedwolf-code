package bundleddriver

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func writeFakeOnefileBinary(t *testing.T, dir, name, version, counterPath string) string {
	t.Helper()
	script := fmt.Sprintf(`#!/bin/sh
set -e
printf 'x\n' >> %q
target="$XDG_CACHE_HOME/opengrep/paintedwolf-%s"
mkdir -p "$target/semgrep/bin"
printf 'engine-%s' > "$target/semgrep/bin/opengrep-core"
chmod 755 "$target/semgrep/bin/opengrep-core"
printf 'schema-%s' > "$target/schema.yaml"
chmod 644 "$target/schema.yaml"
printf '%s\n'
`, counterPath, version, version, version, version)
	bin := filepath.Join(dir, name)
	testutil.FailErr(t, "write fake onefile binary", os.WriteFile(bin, []byte(script), 0o700))
	return bin
}

func countLines(t *testing.T, path string) int {
	t.Helper()
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return 0
	}
	testutil.FailErr(t, "read invocation counter", err)
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return 0
	}
	return len(strings.Split(trimmed, "\n"))
}

func TestPrepareOpengrepRuntimeColdWarmAndConcurrent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell onefile fixture requires macOS or Linux")
	}
	root := t.TempDir()
	store := filepath.Join(root, "runtime-store")
	counter := filepath.Join(root, "invocations")
	bin := writeFakeOnefileBinary(t, root, "opengrep", "1.30.0", counter)

	const workers = 6
	results := make([]string, workers)
	errs := make([]error, workers)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range workers {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-start
			results[idx], errs[idx] = prepareOpengrepRuntimeInStore(t.Context(), bin, store)
		}(i)
	}
	close(start)
	wg.Wait()

	for i := range workers {
		testutil.FailErr(t, fmt.Sprintf("concurrent prepare %d", i), errs[i])
		if results[i] == "" || results[i] != results[0] {
			t.Fatalf("worker %d got %q, want %q", i, results[i], results[0])
		}
	}
	if got := countLines(t, counter); got != 1 {
		t.Fatalf("cold concurrent preparation invoked binary %d times, want 1", got)
	}

	// Warm reuse must hit the verified read-only store without re-invoking bin.
	warm, err := prepareOpengrepRuntimeInStore(t.Context(), bin, store)
	testutil.FailErr(t, "warm prepare", err)
	if warm != results[0] {
		t.Fatalf("warm runtime = %q, want %q", warm, results[0])
	}
	if got := countLines(t, counter); got != 1 {
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
	t.Cleanup(func() { _ = removeReadOnlyTree(store) })
}

func TestPrepareOpengrepRuntimeDetectsTamperingAndRebuilds(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell onefile fixture requires macOS or Linux")
	}
	root := t.TempDir()
	store := filepath.Join(root, "runtime-store")
	counter := filepath.Join(root, "invocations")
	bin := writeFakeOnefileBinary(t, root, "opengrep", "1.30.0", counter)
	t.Cleanup(func() { _ = removeReadOnlyTree(store) })

	cacheDir, err := prepareOpengrepRuntimeInStore(t.Context(), bin, store)
	testutil.FailErr(t, "initial prepare", err)
	if got := countLines(t, counter); got != 1 {
		t.Fatalf("initial invocations = %d, want 1", got)
	}

	// Tamper with a cached executable byte and re-seal it; digest check must
	// reject reuse and rebuild cleanly from the trusted binary.
	corePath := filepath.Join(cacheDir, "opengrep", "paintedwolf-1.30.0", "semgrep", "bin", "opengrep-core")
	testutil.FailErr(t, "unseal core for tamper test", os.Chmod(corePath, 0o600))
	testutil.FailErr(t, "tamper cached engine", os.WriteFile(corePath, []byte("tampered"), 0o600))
	testutil.FailErr(t, "reseal tampered engine", os.Chmod(corePath, 0o555))

	rebuilt, err := prepareOpengrepRuntimeInStore(t.Context(), bin, store)
	testutil.FailErr(t, "rebuild after tamper", err)
	if rebuilt != cacheDir {
		t.Fatalf("rebuilt cacheDir = %q, want %q", rebuilt, cacheDir)
	}
	if got := countLines(t, counter); got != 2 {
		t.Fatalf("invocations after tamper = %d, want 2", got)
	}
	restored, err := os.ReadFile(corePath)
	testutil.FailErr(t, "read restored core", err)
	if string(restored) != "engine-1.30.0" {
		t.Fatalf("restored engine bytes = %q, want %q", restored, "engine-1.30.0")
	}

	// An unexpected extra file inside the cache must also force a clean rebuild.
	unpackedDir := filepath.Join(cacheDir, "opengrep", "paintedwolf-1.30.0")
	testutil.FailErr(t, "unseal unpacked dir", os.Chmod(unpackedDir, 0o700))
	extraPath := filepath.Join(unpackedDir, "injected.so")
	testutil.FailErr(t, "inject extra file", os.WriteFile(extraPath, []byte("evil"), 0o444))
	testutil.FailErr(t, "reseal unpacked dir", os.Chmod(unpackedDir, 0o555))

	_, err = prepareOpengrepRuntimeInStore(t.Context(), bin, store)
	testutil.FailErr(t, "rebuild after injection", err)
	if got := countLines(t, counter); got != 3 {
		t.Fatalf("invocations after injection = %d, want 3", got)
	}
	if _, err := os.Stat(extraPath); !os.IsNotExist(err) {
		t.Fatalf("injected file survived rebuild: err=%v", err)
	}
}

func TestPrepareOpengrepRuntimePrunesStaleVersionDirs(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell onefile fixture requires macOS or Linux")
	}
	root := t.TempDir()
	store := filepath.Join(root, "runtime-store")
	counter := filepath.Join(root, "invocations")
	binV1 := writeFakeOnefileBinary(t, root, "opengrep-v1", "1.30.0", counter)
	binV2 := writeFakeOnefileBinary(t, root, "opengrep-v2", "1.31.0", counter)
	t.Cleanup(func() { _ = removeReadOnlyTree(store) })

	cacheV1, err := prepareOpengrepRuntimeInStore(t.Context(), binV1, store)
	testutil.FailErr(t, "prepare v1", err)
	dirV1 := filepath.Dir(cacheV1)

	cacheV2, err := prepareOpengrepRuntimeInStore(t.Context(), binV2, store)
	testutil.FailErr(t, "prepare v2", err)
	dirV2 := filepath.Dir(cacheV2)

	if dirV1 == dirV2 {
		t.Fatalf("distinct binaries shared runtime dir %q", dirV1)
	}
	if _, err := os.Stat(dirV1); !os.IsNotExist(err) {
		t.Fatalf("stale runtime dir %q was not pruned after version change: err=%v", dirV1, err)
	}
	if _, err := os.Stat(dirV2); err != nil {
		t.Fatalf("new runtime dir %q missing: %v", dirV2, err)
	}
}
