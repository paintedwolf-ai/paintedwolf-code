package bundled_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/scan/bundled"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestStagePreservesFinalizedExecutableAndCompletePayload(t *testing.T) {
	f := newBuildFixture(t)
	m := f.selectArtifact(t)
	root := t.TempDir()
	path, err := bundled.StageArtifact(t.Context(), m, root, f.goos, f.goarch)
	testutil.FailErr(t, "stage finalized artifact", err)
	runtimeManifest := f.runtimeManifest(t)
	got, err := bundled.VerifyStagedArtifact(runtimeManifest, root, f.goos, f.goarch)
	testutil.FailErr(t, "verify complete staged payload", err)
	if got != path {
		t.Fatalf("verified %s, staged %s", got, path)
	}
	_, err = bundled.StageArtifact(t.Context(), m, root, f.goos, f.goarch)
	testutil.FailErr(t, "reuse exact stage", err)
	testutil.FailErr(t, "remove build input", os.RemoveAll(f.directory))
	_, err = bundled.VerifyStagedArtifact(runtimeManifest, root, f.goos, f.goarch)
	testutil.FailErr(t, "runtime independent of build directory", err)
	if _, err := bundled.StageArtifact(t.Context(), m, root, f.goos, f.goarch); err == nil {
		t.Fatal("staging reused stale authority after build input disappeared")
	}
}

func TestRuntimeRejectsTamperedPayloadAndExplicitStagingRepairsIt(t *testing.T) {
	for _, name := range []string{"opengrep", "provenance.json", "source-lock.json", "opengrep-source.tar.gz", "LICENSE", "contracts.jsonl", "platform-checks.json", "runtime.json", "NOTICES-opengrep.md"} {
		t.Run(name, func(t *testing.T) {
			f := newBuildFixture(t)
			m := f.selectArtifact(t)
			root := t.TempDir()
			_, err := bundled.StageArtifact(t.Context(), m, root, f.goos, f.goarch)
			testutil.FailErr(t, "stage", err)
			path := filepath.Join(bundled.EngineBundledDir(root, m.OpenGrep.Version), name)
			if name == "opengrep" {
				path = bundled.ArtifactPath(root, m.OpenGrep.Version, f.goos)
			}
			writeBuildFile(t, filepath.Dir(path), filepath.Base(path), []byte("changed"))
			if _, err := bundled.VerifyStagedArtifact(f.runtimeManifest(t), root, f.goos, f.goarch); err == nil {
				t.Fatal("tampered staged payload accepted")
			}
			if _, err := bundled.ResolveOpenGrepBinary(f.runtimeManifestForHost(t), t.TempDir(), root); err == nil {
				t.Fatal("runtime accepted tampered payload")
			}
			_, err = bundled.StageArtifact(t.Context(), m, root, f.goos, f.goarch)
			testutil.FailErr(t, "repair staged payload from verified source", err)
			_, err = bundled.VerifyStagedArtifact(f.runtimeManifest(t), root, f.goos, f.goarch)
			testutil.FailErr(t, "verify repaired staged payload", err)
		})
	}
}

func TestBuildSelectionRejectsChangedOrIncompletePayload(t *testing.T) {
	for _, name := range []string{"opengrep", "source-lock.json", "opengrep-source.tar.gz", "LICENSE", "contracts.jsonl", "platform-checks.json", "runtime.json"} {
		t.Run(name, func(t *testing.T) {
			f := newBuildFixture(t)
			path := name
			if name == "opengrep" {
				path = filepath.Base(bundled.BinaryPath(f.directory))
			}
			writeBuildFile(t, f.directory, path, []byte("wrong"))
			if _, err := bundled.SelectBuildArtifact(f.source, f.directory, f.goos, f.goarch); err == nil {
				t.Fatal("unqualified bytes accepted")
			}
		})
	}
	f := newBuildFixture(t)
	m := f.selectArtifact(t)
	writeBuildFile(t, f.directory, "LICENSE", []byte("changed"))
	root := filepath.Join(t.TempDir(), "absent")
	if _, err := bundled.StageArtifact(t.Context(), m, root, f.goos, f.goarch); err == nil {
		t.Fatal("changed source input staged")
	}
	if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid input mutated stage: %v", err)
	}
}

func TestCanceledStageDoesNotWrite(t *testing.T) {
	f := newBuildFixture(t)
	m := f.selectArtifact(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	root := filepath.Join(t.TempDir(), "absent")
	if _, err := bundled.StageArtifact(ctx, m, root, f.goos, f.goarch); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled stage: %v", err)
	}
	if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("canceled stage mutated root: %v", err)
	}
}

func TestWindowsTargetSelectsExecutableSuffix(t *testing.T) {
	goos, goarch, err := bundled.PlatformForTarget("x86_64-pc-windows-msvc")
	testutil.FailErr(t, "resolve target", err)
	if goos != "windows" || goarch != "amd64" || filepath.Base(bundled.ArtifactPath(t.TempDir(), "1.29.0+paintedwolf.26", goos)) != "opengrep.exe" {
		t.Fatal("target executable path differs")
	}
}

func TestBuildSelectionRejectsMetadataDriftAndRedirectedPayload(t *testing.T) {
	for _, name := range []string{"opengrep-source.tar.gz", "LICENSE", "provenance.json"} {
		t.Run(name, func(t *testing.T) {
			f := newBuildFixture(t)
			raw, err := os.ReadFile(filepath.Join(f.directory, name))
			testutil.FailErr(t, "read original payload", err)
			target := filepath.Join(t.TempDir(), "redirect")
			testutil.FailErr(t, "write redirected payload", os.WriteFile(target, raw, 0o644))
			testutil.FailErr(t, "remove original", os.Remove(filepath.Join(f.directory, name)))
			testutil.FailErr(t, "redirect payload", os.Symlink(target, filepath.Join(f.directory, name)))
			if _, err := bundled.SelectBuildArtifact(f.source, f.directory, f.goos, f.goarch); err == nil {
				t.Fatal("redirected source payload accepted")
			}
		})
	}
	f := newBuildFixture(t)
	f.provenance["contracts_sha256"] = ""
	f.writeProvenance(t)
	if _, err := bundled.SelectBuildArtifact(f.source, f.directory, f.goos, f.goarch); err == nil {
		t.Fatal("missing qualification digest accepted")
	}
}

func TestStageRepairRejectsSymlinksAndDirectories(t *testing.T) {
	for _, kind := range []string{"symlink", "directory"} {
		t.Run(kind, func(t *testing.T) {
			f := newReleaseFixture(t)
			resolved := f.fetch(t, bundled.ReleaseFetchOptions{CacheRoot: t.TempDir(), Archive: f.archive})
			root := t.TempDir()
			_, err := bundled.StageArtifact(t.Context(), resolved.Manifest, root, f.goos, f.goarch)
			testutil.FailErr(t, "stage released payload", err)
			path := filepath.Join(bundled.EngineBundledDir(root, f.source.OpenGrep.Version), "NOTICES-opengrep.md")
			testutil.FailErr(t, "remove stage fixture", os.Remove(path))
			target := filepath.Join(t.TempDir(), "outside")
			writeBuildFile(t, filepath.Dir(target), filepath.Base(target), []byte("preserve outside"))
			if kind == "symlink" {
				testutil.FailErr(t, "redirect staged payload", os.Symlink(target, path))
			} else {
				testutil.FailErr(t, "replace payload with directory", os.Mkdir(path, 0o700))
			}
			if _, err := bundled.StageArtifact(t.Context(), resolved.Manifest, root, f.goos, f.goarch); err == nil {
				t.Fatal("nonregular stage was repaired")
			}
			raw, err := os.ReadFile(target)
			testutil.FailErr(t, "read outside target", err)
			if string(raw) != "preserve outside" {
				t.Fatal("stage repair changed outside target")
			}
		})
	}
}

func TestStageRepairRequiresUnchangedAdmittedSource(t *testing.T) {
	f := newReleaseFixture(t)
	resolved := f.fetch(t, bundled.ReleaseFetchOptions{CacheRoot: t.TempDir(), Archive: f.archive})
	root := t.TempDir()
	binary, err := bundled.StageArtifact(t.Context(), resolved.Manifest, root, f.goos, f.goarch)
	testutil.FailErr(t, "stage release", err)
	writeBuildFile(t, filepath.Dir(binary), filepath.Base(binary), []byte("stale destination"))
	writeBuildFile(t, resolved.Directory, "opengrep", []byte("changed source"))
	if _, err := bundled.StageArtifact(t.Context(), resolved.Manifest, root, f.goos, f.goarch); err == nil {
		t.Fatal("changed source repaired staged payload")
	}
	raw, err := os.ReadFile(binary)
	testutil.FailErr(t, "read preserved destination", err)
	if string(raw) != "stale destination" {
		t.Fatal("failed repair changed destination")
	}
}

func TestStageRepairsExecutablePermissionsFromAdmittedRelease(t *testing.T) {
	f := newReleaseFixture(t)
	resolved := f.fetch(t, bundled.ReleaseFetchOptions{CacheRoot: t.TempDir(), Archive: f.archive})
	root := t.TempDir()
	binary, err := bundled.StageArtifact(t.Context(), resolved.Manifest, root, f.goos, f.goarch)
	testutil.FailErr(t, "stage release", err)
	testutil.FailErr(t, "remove executable permission", os.Chmod(binary, 0o644))
	_, err = bundled.StageArtifact(t.Context(), resolved.Manifest, root, f.goos, f.goarch)
	testutil.FailErr(t, "repair executable permission", err)
	_, err = bundled.VerifyStagedArtifact(resolved.Manifest, root, f.goos, f.goarch)
	testutil.FailErr(t, "verify repaired executable", err)
}

func TestStageRejectsRedirectedPayloadDirectory(t *testing.T) {
	f := newReleaseFixture(t)
	resolved := f.fetch(t, bundled.ReleaseFetchOptions{CacheRoot: t.TempDir(), Archive: f.archive})
	root := t.TempDir()
	_, err := bundled.StageArtifact(t.Context(), resolved.Manifest, root, f.goos, f.goarch)
	testutil.FailErr(t, "stage release", err)
	directory := bundled.EngineBundledDir(root, f.source.OpenGrep.Version)
	moved := filepath.Join(t.TempDir(), "moved payload")
	testutil.FailErr(t, "move staged fixture", os.Rename(directory, moved))
	testutil.FailErr(t, "redirect stage directory", os.Symlink(moved, directory))
	if _, err := bundled.StageArtifact(t.Context(), resolved.Manifest, root, f.goos, f.goarch); err == nil {
		t.Fatal("redirected stage directory accepted for repair")
	}
	if _, err := bundled.VerifyStagedArtifact(resolved.Manifest, root, f.goos, f.goarch); err == nil {
		t.Fatal("redirected stage directory accepted at runtime")
	}
	if _, err := bundled.ResolveOpenGrepBinary(f.runtimeManifestForHost(t), t.TempDir(), root); err == nil {
		t.Fatal("runtime resolved redirected staged directory")
	}
}
