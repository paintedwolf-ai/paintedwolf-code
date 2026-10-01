package gitengine

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestPinnedVersionFromManifest(t *testing.T) {
	resetForTest()
	got := PinnedVersion()
	if got == "" {
		t.Fatal("PinnedVersion returned empty")
	}
	raw, err := os.ReadFile(filepath.Join("..", "..", "config", "gitengine", "pin.yaml"))
	testutil.FailErr(t, "read pin.yaml", err)
	if !strings.Contains(string(raw), "git_version: \""+got+"\"") && !strings.Contains(string(raw), "git_version: "+got) {
		t.Fatalf("PinnedVersion %q not present in pin.yaml", got)
	}
	src, err := os.ReadFile("pin.go")
	testutil.FailErr(t, "read pin.go", err)
	if strings.Contains(string(src), "\""+got+"\"") {
		t.Fatalf("pin.go must not hard-code git version %q", got)
	}
}

func TestResolvePrefersStructuredEngineRoot(t *testing.T) {
	resetForTest()
	t.Setenv(EnvGitBinary, "")
	t.Setenv(EnvTest, "")

	engineRoot := t.TempDir()
	t.Setenv("LYCAON_ENGINE_ROOT", engineRoot)
	gitRel, _ := engineBinaryPaths(runtime.GOOS)
	bin := filepath.Join(engineRoot, "gitengine", gitRel)
	testutil.FailErr(t, "mkdir", os.MkdirAll(filepath.Dir(bin), 0o755))
	writeStubGit(t, bin, pinnedReportedVersion(t))
	versionOfFn = func(path string) (string, error) {
		if path != bin {
			t.Fatalf("unexpected version probe path %s", path)
		}
		return pinnedReportedVersion(t), nil
	}

	got, err := BinaryPath()
	testutil.FailErr(t, "BinaryPath", err)
	if got != bin {
		t.Fatalf("BinaryPath = %q, want engine-root binary %q", got, bin)
	}
	lfs, err := LFSPath()
	testutil.FailErr(t, "LFSPath", err)
	_, lfsRel := engineBinaryPaths(runtime.GOOS)
	if want := filepath.Join(engineRoot, "gitengine", lfsRel); lfs != want {
		t.Fatalf("LFSPath = %q, want %q", lfs, want)
	}
}

func TestBundleLayoutByOperatingSystem(t *testing.T) {
	t.Parallel()
	root := filepath.Join("root", "app")
	tests := []struct {
		goos   string
		gitRel string
		lfsRel string
	}{
		{goos: "darwin", gitRel: filepath.Join("bin", "git"), lfsRel: filepath.Join("bin", "git-lfs")},
		{goos: "linux", gitRel: filepath.Join("bin", "git"), lfsRel: filepath.Join("bin", "git-lfs")},
		{goos: "windows", gitRel: filepath.Join("cmd", "git.exe"), lfsRel: filepath.Join("mingw64", "libexec", "git-core", "git-lfs.exe")},
	}
	for _, tt := range tests {
		t.Run(tt.goos, func(t *testing.T) {
			t.Parallel()
			gitRel, lfsRel := engineBinaryPaths(tt.goos)
			if gitRel != tt.gitRel || lfsRel != tt.lfsRel {
				t.Fatalf("engineBinaryPaths(%q) = %q, %q; want %q, %q", tt.goos, gitRel, lfsRel, tt.gitRel, tt.lfsRel)
			}
			gitBin := filepath.Join(root, gitRel)
			if got := lfsForBinary(gitBin, tt.goos); got != filepath.Join(root, lfsRel) {
				t.Fatalf("lfsForBinary(%q, %q) = %q, want %q", gitBin, tt.goos, got, filepath.Join(root, lfsRel))
			}
		})
	}
}

func TestPlatformPinKeys(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		goos, goarch, want string
	}{
		{goos: "darwin", goarch: "arm64", want: "darwin-arm64"},
		{goos: "linux", goarch: "amd64", want: "linux-amd64"},
		{goos: "linux", goarch: "arm64", want: "linux-arm64"},
		{goos: "windows", goarch: "amd64", want: "windows-amd64"},
	} {
		if got := platformKey(tt.goos, tt.goarch); got != tt.want {
			t.Fatalf("platformKey(%q, %q) = %q, want %q", tt.goos, tt.goarch, got, tt.want)
		}
	}
}

func TestResolveNoPathFallback(t *testing.T) {
	resetForTest()
	t.Setenv(EnvGitBinary, "")
	t.Setenv(EnvTest, "")
	t.Setenv("LYCAON_ENGINE_ROOT", "")

	_, err := BinaryPath()
	var ue *UnavailableError
	if !errors.As(err, &ue) {
		t.Fatalf("err = %v (%T), want UnavailableError", err, err)
	}
	if ue.Reason != "missing" {
		t.Fatalf("Reason = %q, want missing", ue.Reason)
	}
}

func TestTestSeamRequiresTestEnv(t *testing.T) {
	resetForTest()
	root := t.TempDir()
	seam := filepath.Join(root, "seam-git")
	writeStubGit(t, seam, pinnedReportedVersion(t))

	t.Setenv("LYCAON_ENGINE_ROOT", "")
	versionOfFn = func(path string) (string, error) { return pinnedReportedVersion(t), nil }

	t.Setenv(EnvGitBinary, seam)
	t.Setenv(EnvTest, "")
	_, err := BinaryPath()
	var ignored *UnavailableError
	if !errors.As(err, &ignored) {
		t.Fatalf("without LYCAON_TEST, seam must be ignored; err=%v", err)
	}

	resetForTest()
	versionOfFn = func(path string) (string, error) {
		if path != seam {
			return "", fmt.Errorf("unexpected path %s", path)
		}
		return pinnedReportedVersion(t), nil
	}
	t.Setenv(EnvGitBinary, seam)
	t.Setenv(EnvTest, "1")
	got, err := BinaryPath()
	testutil.FailErr(t, "BinaryPath with test seam", err)
	if got != seam {
		t.Fatalf("BinaryPath = %q, want seam %q", got, seam)
	}
}

func TestVersionMismatchIsError(t *testing.T) {
	resetForTest()
	t.Setenv(EnvGitBinary, "")
	t.Setenv(EnvTest, "")

	stageEngineGit(t, "0.0.1")
	versionOfFn = versionOf

	_, err := BinaryPath()
	var ue *UnavailableError
	if !errors.As(err, &ue) {
		t.Fatalf("err = %v (%T), want UnavailableError", err, err)
	}
	if ue.Reason != "version_mismatch" {
		t.Fatalf("Reason = %q, want version_mismatch", ue.Reason)
	}
	if ue.Expected != pinnedReportedVersion(t) {
		t.Fatalf("Expected = %q, want %q", ue.Expected, pinnedReportedVersion(t))
	}
	if ue.Found != "0.0.1" {
		t.Fatalf("Found = %q, want 0.0.1", ue.Found)
	}
}

func TestVersionAssertedOnce(t *testing.T) {
	resetForTest()
	t.Setenv(EnvGitBinary, "")
	t.Setenv(EnvTest, "")

	_, bin := stageEngineGit(t, pinnedReportedVersion(t))

	var probes atomic.Int32
	versionOfFn = func(path string) (string, error) {
		probes.Add(1)
		return pinnedReportedVersion(t), nil
	}

	for i := 0; i < 2; i++ {
		got, err := BinaryPath()
		testutil.FailErr(t, "BinaryPath", err)
		if got != bin {
			t.Fatalf("call %d: BinaryPath = %q", i, got)
		}
	}
	if n := probes.Load(); n != 1 {
		t.Fatalf("--version probed %d times, want 1", n)
	}
}

func TestEmptyHooksDirIsEmptyAndRestrictive(t *testing.T) {
	resetForTest()
	cfg := t.TempDir()
	t.Setenv("LYCAON_CONFIG_DIR", cfg)

	dir, err := EmptyHooksDir()
	testutil.FailErr(t, "EmptyHooksDir", err)

	st, err := os.Stat(dir)
	testutil.FailErr(t, "stat hooks dir", err)
	if !st.IsDir() {
		t.Fatalf("%s is not a directory", dir)
	}
	if perm := st.Mode().Perm(); perm != 0o500 {
		t.Fatalf("mode = %04o, want 0500", perm)
	}
	ents, err := os.ReadDir(dir)
	testutil.FailErr(t, "readdir", err)
	if len(ents) != 0 {
		t.Fatalf("hooks dir has %d entries, want 0", len(ents))
	}

	testutil.FailErr(t, "chmod writable", os.Chmod(dir, 0o700))
	plant := filepath.Join(dir, "pre-commit")
	testutil.FailErr(t, "plant hook", os.WriteFile(plant, []byte("evil"), 0o644))
	testutil.FailErr(t, "chmod restore", os.Chmod(dir, 0o500))
	resetForTest()
	t.Setenv("LYCAON_CONFIG_DIR", cfg)
	_, err = EmptyHooksDir()
	if err == nil {
		t.Fatal("EmptyHooksDir succeeded with a planted file")
	}

	testutil.FailErr(t, "chmod cleanup", os.Chmod(dir, 0o700))
	testutil.FailErr(t, "remove plant", os.Remove(plant))
}

func TestEmptyHooksDirRechecksEachCall(t *testing.T) {
	resetForTest()
	cfg := t.TempDir()
	t.Setenv("LYCAON_CONFIG_DIR", cfg)

	dir, err := EmptyHooksDir()
	testutil.FailErr(t, "EmptyHooksDir", err)

	testutil.FailErr(t, "chmod for removal", os.Chmod(dir, 0o700))
	testutil.FailErr(t, "remove hooks dir", os.RemoveAll(dir))

	again, err := EmptyHooksDir()
	testutil.FailErr(t, "EmptyHooksDir after removal", err)
	if again != dir {
		t.Fatalf("recreated at %s, want %s", again, dir)
	}
	st, err := os.Stat(again)
	testutil.FailErr(t, "stat recreated", err)
	if perm := st.Mode().Perm(); perm != 0o500 {
		t.Fatalf("recreated mode = %04o, want 0500", perm)
	}

	testutil.FailErr(t, "chmod writable", os.Chmod(dir, 0o700))
	plant := filepath.Join(dir, "post-checkout")
	testutil.FailErr(t, "plant hook", os.WriteFile(plant, []byte("evil"), 0o600))
	testutil.FailErr(t, "chmod restore", os.Chmod(dir, 0o500))
	if _, err := EmptyHooksDir(); err == nil {
		t.Fatal("EmptyHooksDir succeeded with a file planted after first use")
	}

	testutil.FailErr(t, "chmod cleanup", os.Chmod(dir, 0o700))
	testutil.FailErr(t, "remove plant", os.Remove(plant))
}

func writeStubGit(t *testing.T, path, version string) {
	t.Helper()
	script := fmt.Sprintf("#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then\n  echo 'git version %s'\n  exit 0\nfi\necho 'unexpected' >&2\nexit 1\n", version)
	testutil.FailErr(t, "write stub", os.WriteFile(path, []byte(script), 0o755))
}

func pinnedReportedVersion(t *testing.T) string {
	t.Helper()
	version, err := loadPinnedReportedVersion()
	testutil.FailErr(t, "load reported Git version", err)
	return version
}

func stageEngineGit(t *testing.T, version string) (engineRoot, bin string) {
	t.Helper()
	engineRoot = t.TempDir()
	t.Setenv("LYCAON_ENGINE_ROOT", engineRoot)
	gitRel, _ := engineBinaryPaths(runtime.GOOS)
	bin = filepath.Join(engineRoot, "gitengine", gitRel)
	testutil.FailErr(t, "mkdir staged Git", os.MkdirAll(filepath.Dir(bin), 0o755))
	writeStubGit(t, bin, version)
	return engineRoot, bin
}
