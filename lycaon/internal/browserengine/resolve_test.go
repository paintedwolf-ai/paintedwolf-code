package browserengine

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestResolveBinaryEngineRoot(t *testing.T) {
	t.Setenv(EnvBrowserBin, "")
	root := t.TempDir()
	t.Setenv(configlayout.EnvEngineRoot, root)
	dir := filepath.Join(root, "browser")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		testutil.FailErr(t, "create bundled browser directory", err)
	}
	bin := filepath.Join(dir, headlessShellName())
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		testutil.FailErr(t, "write bundled browser", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "icudtl.dat"), []byte("icu"), 0o644); err != nil {
		testutil.FailErr(t, "write bundled browser resources", err)
	}
	got, ok := ResolveBinary(ResolveOptions{CacheDir: t.TempDir()})
	if !ok || got.Source != SourceBundle || got.Path != bin {
		t.Fatalf("resolve from engine root: got %+v, ok=%t", got, ok)
	}
}

func TestResolveBinaryEnvWins(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, headlessShellName())
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	// The executable requires its sibling resource files.
	if err := os.WriteFile(filepath.Join(dir, "icudtl.dat"), []byte("icu"), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	t.Setenv(EnvBrowserBin, bin)
	t.Setenv(EnvBrowserSkipDownload, "1")

	got, ok := ResolveBinary(ResolveOptions{CacheDir: t.TempDir()})
	if !ok {
		t.Fatal("expected resolve")
	}
	if got.Source != SourceEnv || got.Path != bin {
		t.Fatalf("got %+v", got)
	}
}

func TestResolveBinaryManaged(t *testing.T) {
	t.Setenv(EnvBrowserBin, "")
	t.Setenv(configlayout.EnvEngineRoot, "")
	cache := t.TempDir()
	root := managedRoot(cache)
	if root == "" {
		t.Skip("unsupported platform")
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		testutil.FailErr(t, "create directory", err)
	}
	bin := filepath.Join(root, headlessShellName())
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	if err := os.WriteFile(filepath.Join(root, "icudtl.dat"), []byte("icu"), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}

	got, ok := ResolveBinary(ResolveOptions{CacheDir: cache})
	if !ok {
		t.Fatal("expected managed resolve")
	}
	if got.Source != SourceManaged {
		t.Fatalf("source=%s", got.Source)
	}
}

func TestCFTPlatformKnown(t *testing.T) {
	_, err := cftPlatform()
	switch runtime.GOOS + "/" + runtime.GOARCH {
	case "darwin/arm64", "darwin/amd64", "linux/amd64", "linux/arm64", "windows/amd64", "windows/386":
		if err != nil {
			t.Fatal(err)
		}
	default:
		if err == nil {
			t.Fatal("expected unsupported platform error")
		}
	}
}
