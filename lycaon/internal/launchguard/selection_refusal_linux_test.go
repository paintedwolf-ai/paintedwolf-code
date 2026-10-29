//go:build linux

package launchguard

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/lycaon/lycaon/internal/pkgmanifest"
	"github.com/lycaon/lycaon/internal/platformfloor"
	"github.com/lycaon/lycaon/internal/testutil"
)

var bootstrapRoles = []string{"painted-wolf-code", "pw-launch-entry"}

// controlledAnchor selects a fresh root-controlled trust anchor whose policy
// and lock locations live below it.
func controlledAnchor(t *testing.T) string {
	t.Helper()
	base := filepath.Join(t.TempDir(), "anchor")
	controlledDir(t, base)
	withTestTrustAnchor(t, base)
	policyPath = filepath.Join(base, "launch.json")
	lockDirectory = filepath.Join(base, "locks")
	return base
}

func controlledDir(t *testing.T, dir string) {
	t.Helper()
	testutil.FailErr(t, "mkdir "+dir, os.MkdirAll(dir, 0o755))
	testutil.FailErr(t, "chmod "+dir, os.Chmod(dir, 0o755))
}

func controlledFile(t *testing.T, path string, content []byte, mode os.FileMode) {
	t.Helper()
	testutil.FailErr(t, "write "+path, os.WriteFile(path, content, mode))
	testutil.FailErr(t, "chmod "+path, os.Chmod(path, mode))
}

type generationFixture struct {
	directory string
	policy    GenerationPolicy
}

// installGeneration stages a generation whose manifest declares the given
// roles while only the present ones exist on disk.
func installGeneration(t *testing.T, base string, declared, present []string) generationFixture {
	t.Helper()
	install := filepath.Join(base, "install")
	staging := filepath.Join(base, "staging")
	controlledDir(t, filepath.Join(staging, "bin"))
	content := []byte("#!/bin/sh\n")
	sum := sha256.Sum256(content)
	contentDigest := hex.EncodeToString(sum[:])
	size := int64(len(content))
	var executables []pkgmanifest.Executable
	payload := []pkgmanifest.PayloadEntry{{Path: "bin", Kind: "directory", Mode: 0o755}}
	for _, name := range declared {
		executables = append(executables, pkgmanifest.Executable{Path: filepath.Join(install, "bin", name), SHA256: contentDigest, Size: size})
		payload = append(payload, pkgmanifest.PayloadEntry{Path: "bin/" + name, Kind: "file", Mode: 0o755, SHA256: &contentDigest, Size: &size})
	}
	for _, name := range present {
		controlledFile(t, filepath.Join(staging, "bin", name), content, 0o755)
	}
	arch, _ := platformfloor.ELFArchForGoArch(runtime.GOARCH)
	manifest, err := json.Marshal(pkgmanifest.Manifest{
		FormatVersion: pkgmanifest.FormatVersion,
		Package:       "painted-wolf-code",
		Version:       "1.0.0",
		Architecture:  arch,
		InstallRoot:   install,
		Executables:   executables,
		Payload:       payload,
	})
	testutil.FailErr(t, "marshal manifest", err)
	return stageGeneration(t, base, staging, manifest)
}

// stageGeneration places the staged tree at the generation directory the
// manifest digest names and returns the policy selecting it.
func stageGeneration(t *testing.T, base, staging string, manifest []byte) generationFixture {
	t.Helper()
	controlledFile(t, filepath.Join(staging, "manifest.json"), manifest, 0o644)
	sum := sha256.Sum256(manifest)
	digest := hex.EncodeToString(sum[:])
	controlledDir(t, filepath.Join(base, "generations"))
	directory := filepath.Join(base, "generations", digest)
	testutil.FailErr(t, "place generation", os.Rename(staging, directory))
	return generationFixture{
		directory: directory,
		policy:    GenerationPolicy{Version: 1, GenerationRoot: directory, ManifestSHA256: digest, LogicalInstallRoot: filepath.Join(base, "install"), LibraryRoots: []string{fixtureLibraryRoot}},
	}
}

func TestControlledPathRefusesBrokenBasesAndUncreatableComponents(t *testing.T) {
	base := controlledAnchor(t)
	closed, err := os.Open(base)
	testutil.FailErr(t, "open anchor", err)
	testutil.FailErr(t, "close anchor", closed.Close())
	if file, err := openControlledAt(closed, "child", true); err == nil {
		_ = file.Close()
		t.Fatal("openControlledAt walked from a closed base")
	}
	if dir, err := ensureControlledDirectory(closed, "/child", trustedObject); err == nil {
		_ = dir.Close()
		t.Fatal("ensureControlledDirectory created below a closed base")
	}
	anchor, err := os.Open(base)
	testutil.FailErr(t, "open anchor", err)
	defer func() { _ = anchor.Close() }()
	dir, err := ensureControlledDirectory(anchor, "/"+strings.Repeat("n", 256), trustedObject)
	if !errors.Is(err, unix.ENAMETOOLONG) {
		if err == nil {
			_ = dir.Close()
		}
		t.Fatalf("uncreatable component: got %v, want ENAMETOOLONG", err)
	}
}

func TestRetainGenerationRefusesUnusableLockLocations(t *testing.T) {
	base := controlledAnchor(t)
	digest := strings.Repeat("a", 64)
	lockDirectory = base
	if lock, err := retainGeneration(digest); err == nil {
		_ = lock.Close()
		t.Fatal("retained a lock in the trust anchor itself")
	}
	lockDirectory = filepath.Join(base, "locks")
	if lock, err := retainGeneration("nested/" + digest); err == nil {
		_ = lock.Close()
		t.Fatal("retained a lock below a missing component")
	}
	controlledFile(t, filepath.Join(lockDirectory, digest+".lock"), nil, 0o666)
	if lock, err := retainGeneration(digest); err == nil {
		_ = lock.Close()
		t.Fatal("retained a group-writable lock file")
	}
}

func TestCaptureGenerationRefusesEveryBrokenSelectionStep(t *testing.T) {
	refuse := func(t *testing.T, what string) {
		t.Helper()
		if generation, err := CaptureGeneration(); err == nil {
			generation.Close()
			t.Fatalf("CaptureGeneration accepted %s", what)
		}
	}
	t.Run("missing policy", func(t *testing.T) {
		controlledAnchor(t)
		refuse(t, "a missing policy")
	})
	t.Run("unusable lock directory", func(t *testing.T) {
		base := controlledAnchor(t)
		fixture := installGeneration(t, base, bootstrapRoles, bootstrapRoles)
		testutil.FailErr(t, "select generation", WritePolicy(fixture.policy))
		controlledFile(t, filepath.Join(base, "locks-file"), nil, 0o644)
		lockDirectory = filepath.Join(base, "locks-file")
		refuse(t, "a lock directory that is a regular file")
	})
	t.Run("absent generation root", func(t *testing.T) {
		base := controlledAnchor(t)
		fixture := installGeneration(t, base, bootstrapRoles, bootstrapRoles)
		fixture.policy.GenerationRoot = filepath.Join(base, "generations", "absent")
		testutil.FailErr(t, "select generation", WritePolicy(fixture.policy))
		refuse(t, "an absent generation root")
	})
	t.Run("missing manifest", func(t *testing.T) {
		base := controlledAnchor(t)
		fixture := installGeneration(t, base, bootstrapRoles, bootstrapRoles)
		testutil.FailErr(t, "select generation", WritePolicy(fixture.policy))
		testutil.FailErr(t, "remove manifest", os.Remove(filepath.Join(fixture.directory, "manifest.json")))
		refuse(t, "a generation without a manifest")
	})
	t.Run("group-writable manifest", func(t *testing.T) {
		base := controlledAnchor(t)
		fixture := installGeneration(t, base, bootstrapRoles, bootstrapRoles)
		testutil.FailErr(t, "select generation", WritePolicy(fixture.policy))
		testutil.FailErr(t, "loosen manifest", os.Chmod(filepath.Join(fixture.directory, "manifest.json"), 0o664))
		refuse(t, "a group-writable manifest")
	})
	t.Run("unparseable manifest", func(t *testing.T) {
		base := controlledAnchor(t)
		staging := filepath.Join(base, "staging")
		controlledDir(t, staging)
		fixture := stageGeneration(t, base, staging, []byte("{\n"))
		testutil.FailErr(t, "select generation", WritePolicy(fixture.policy))
		refuse(t, "a manifest that does not parse")
	})
	t.Run("manifest omits the entry role", func(t *testing.T) {
		base := controlledAnchor(t)
		fixture := installGeneration(t, base, bootstrapRoles[:1], bootstrapRoles)
		testutil.FailErr(t, "select generation", WritePolicy(fixture.policy))
		refuse(t, "a manifest without the entry role")
	})
	t.Run("entry image missing", func(t *testing.T) {
		base := controlledAnchor(t)
		fixture := installGeneration(t, base, bootstrapRoles, bootstrapRoles[:1])
		testutil.FailErr(t, "select generation", WritePolicy(fixture.policy))
		refuse(t, "a generation whose entry image is absent")
	})
	t.Run("entry image group-writable", func(t *testing.T) {
		base := controlledAnchor(t)
		fixture := installGeneration(t, base, bootstrapRoles, bootstrapRoles)
		testutil.FailErr(t, "select generation", WritePolicy(fixture.policy))
		testutil.FailErr(t, "loosen entry image", os.Chmod(filepath.Join(fixture.directory, "bin", "pw-launch-entry"), 0o775))
		refuse(t, "a group-writable entry image")
	})
}
