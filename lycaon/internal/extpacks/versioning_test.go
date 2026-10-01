package extpacks_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/extensionstate"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/extpackstest"
	"github.com/lycaon/lycaon/internal/testutil/extstatetest"
	"gopkg.in/yaml.v3"
)

// installDevice submits one install through the subsystem owner.
func installDevice(t *testing.T, source, version string) extensionstate.Result {
	t.Helper()
	return extstatetest.Apply(t, extstatetest.Owner(t), extstatetest.DeviceScope(),
		extensionstate.InstallOp{Source: source, Version: version})
}

func writeDesiredForTest(t *testing.T, path string, d extpacks.DesiredState) {
	t.Helper()
	data, err := extpacks.EncodeDesired(d)
	testutil.FailErr(t, "encode desired", err)
	testutil.FailErr(t, "mkdir desired dir", os.MkdirAll(filepath.Dir(path), 0o700))
	testutil.FailErr(t, "write desired", os.WriteFile(path, data, 0o600))
}

func TestInstallReleaseSelectsHighestMatchingVersionAndLocksExactContent(t *testing.T) {
	t.Setenv("LYCAON_CONFIG_DIR", t.TempDir())
	repo := filepath.Join(t.TempDir(), "releases")
	writeReleasePack(t, repo, "acme/releases", "1.0.0", nil)
	extpackstest.GitInitCommit(t, repo)
	extpackstest.GitTag(t, repo, "v1.0.0")
	writeReleasePack(t, repo, "acme/releases", "1.4.0", nil)
	extpackstest.GitCommitAll(t, repo, "release 1.4.0")
	extpackstest.GitTag(t, repo, "v1.4.0")
	extpackstest.GitTag(t, repo, "1.4.0")
	writeReleasePack(t, repo, "acme/releases", "2.0.0", nil)
	extpackstest.GitCommitAll(t, repo, "release 2.0.0")
	extpackstest.GitTag(t, repo, "v2.0.0")

	result := installDevice(t, "file://"+repo, "^1.0.0")
	if result.Install == nil || result.Install.Version != "1.4.0" {
		t.Fatalf("selected resolution = %+v, want version 1.4.0", result.Install)
	}
	if _, err := os.Stat(filepath.Join(result.PackageRoot, ".git")); !os.IsNotExist(err) {
		t.Fatalf("cached body contains repository metadata: %v", err)
	}
	lockPath, err := extpacks.DeviceLockPath()
	testutil.FailErr(t, "DeviceLockPath", err)
	lock, err := extpacks.LoadLockFile(lockPath)
	testutil.FailErr(t, "LoadLockFile", err)
	locked, ok := lock.Package("acme/releases")
	if !ok || locked.Version != "1.4.0" || locked.Ref != "1.4.0" || locked.Revision == "" || locked.Integrity == "" {
		t.Fatalf("locked release = %+v, present=%v", locked, ok)
	}
	desiredPath, err := extpacks.DeviceDesiredPath()
	testutil.FailErr(t, "DeviceDesiredPath", err)
	desired, err := extpacks.LoadDesiredFile(desiredPath)
	testutil.FailErr(t, "LoadDesiredFile", err)
	row, ok := extpacks.DesiredPackRow(desired, "acme/releases")
	if !ok || row.Version != "^1.0.0" || row.Ref != "" || row.Development {
		t.Fatalf("desired release = %+v, present=%v", row, ok)
	}
}

func TestFileURLDecodesLocalPath(t *testing.T) {
	t.Setenv("LYCAON_CONFIG_DIR", t.TempDir())
	repo := filepath.Join(t.TempDir(), "release pack")
	writeReleasePack(t, repo, "acme/file-url", "1.0.0", nil)
	extpackstest.GitInitCommit(t, repo)
	extpackstest.GitTag(t, repo, "v1.0.0")

	result := installDevice(t, "file://"+strings.ReplaceAll(repo, " ", "%20"), "1.0.0")
	if result.Install == nil || result.Install.PackID != "acme/file-url" {
		t.Fatalf("install result = %+v", result.Install)
	}
}

func TestFileURLRejectsRemoteAuthority(t *testing.T) {
	t.Setenv("LYCAON_CONFIG_DIR", t.TempDir())
	_, err := extpacks.PrepareInstall(t.Context(), extpacks.InstallOptions{
		Source: "file://remote.example/pack",
	})
	if err == nil || !strings.Contains(err.Error(), "local file URL") {
		t.Fatalf("remote authority error = %v", err)
	}
}

func TestReleaseResolverBacktracksAcrossTransitiveConstraints(t *testing.T) {
	t.Setenv("LYCAON_CONFIG_DIR", t.TempDir())
	base := t.TempDir()
	cRepo := filepath.Join(base, "c")
	writeReleasePack(t, cRepo, "acme/c", "1.0.0", nil)
	extpackstest.GitInitCommit(t, cRepo)
	extpackstest.GitTag(t, cRepo, "v1.0.0")
	writeReleasePack(t, cRepo, "acme/c", "2.0.0", nil)
	extpackstest.GitCommitAll(t, cRepo, "release c2")
	extpackstest.GitTag(t, cRepo, "v2.0.0")

	aRepo := filepath.Join(base, "a")
	writeReleasePack(t, aRepo, "acme/a", "1.0.0", deps("acme/c", cRepo, "^1.0.0"))
	extpackstest.GitInitCommit(t, aRepo)
	extpackstest.GitTag(t, aRepo, "v1.0.0")
	writeReleasePack(t, aRepo, "acme/a", "2.0.0", deps("acme/c", cRepo, "^2.0.0"))
	extpackstest.GitCommitAll(t, aRepo, "release a2")
	extpackstest.GitTag(t, aRepo, "v2.0.0")

	bRepo := filepath.Join(base, "b")
	writeReleasePack(t, bRepo, "acme/b", "1.0.0", deps("acme/c", cRepo, "^1.0.0"))
	extpackstest.GitInitCommit(t, bRepo)
	extpackstest.GitTag(t, bRepo, "v1.0.0")

	rootRepo := filepath.Join(base, "root")
	rootDeps := map[string]extpacks.DependencyRequest{
		"acme/a": {Source: "file://" + aRepo, Version: "*"},
		"acme/b": {Source: "file://" + bRepo, Version: "*"},
	}
	writeReleasePack(t, rootRepo, "acme/root", "1.0.0", rootDeps)
	extpackstest.GitInitCommit(t, rootRepo)
	extpackstest.GitTag(t, rootRepo, "v1.0.0")

	installDevice(t, "file://"+rootRepo, "*")
	lockPath, err := extpacks.DeviceLockPath()
	testutil.FailErr(t, "DeviceLockPath", err)
	lock, err := extpacks.LoadLockFile(lockPath)
	testutil.FailErr(t, "LoadLockFile", err)
	for id, want := range map[string]string{
		"acme/root": "1.0.0", "acme/a": "1.0.0", "acme/b": "1.0.0", "acme/c": "1.0.0",
	} {
		pkg, ok := lock.Package(id)
		if !ok || pkg.Version != want {
			t.Fatalf("lock %s = %+v, present=%v, want version %s", id, pkg, ok, want)
		}
	}
}

func TestInstallDoesNotMoveAnUnrelatedStarRoot(t *testing.T) {
	t.Setenv("LYCAON_CONFIG_DIR", t.TempDir())
	aRepo := filepath.Join(t.TempDir(), "pack-a")
	writeReleasePack(t, aRepo, "acme/pack-a", "1.0.0", nil)
	extpackstest.GitInitCommit(t, aRepo)
	extpackstest.GitTag(t, aRepo, "v1.0.0")

	installDevice(t, "file://"+aRepo, "*")
	writeReleasePack(t, aRepo, "acme/pack-a", "1.1.0", nil)
	extpackstest.GitCommitAll(t, aRepo, "release 1.1.0")
	extpackstest.GitTag(t, aRepo, "v1.1.0")

	bRepo := filepath.Join(t.TempDir(), "pack-b")
	writeReleasePack(t, bRepo, "acme/pack-b", "1.0.0", nil)
	extpackstest.GitInitCommit(t, bRepo)
	extpackstest.GitTag(t, bRepo, "v1.0.0")
	installDevice(t, "file://"+bRepo, "*")

	lockPath, err := extpacks.DeviceLockPath()
	testutil.FailErr(t, "DeviceLockPath", err)
	lock, err := extpacks.LoadLockFile(lockPath)
	testutil.FailErr(t, "LoadLockFile after unrelated install", err)
	lockedA, ok := lock.Package("acme/pack-a")
	if !ok || lockedA.Version != "1.0.0" {
		t.Fatalf("pack-a after installing pack-b = %+v present=%v, want 1.0.0", lockedA, ok)
	}

	result := extstatetest.Apply(t, extstatetest.Owner(t), extstatetest.DeviceScope(),
		extensionstate.UpdateOp{PackID: "acme/pack-a"})
	lock, err = extpacks.LoadLockFile(lockPath)
	testutil.FailErr(t, "LoadLockFile after update", err)
	lockedA, ok = lock.Package("acme/pack-a")
	if !ok || lockedA.Version != "1.1.0" {
		t.Fatalf("pack-a after update = %+v present=%v, want 1.1.0", lockedA, ok)
	}
	if result.Install == nil || result.Install.Version != "1.1.0" {
		t.Fatalf("update resolution = %+v, want 1.1.0", result.Install)
	}
}

func TestInstallingAnotherRootHoldsExistingSharedConstraint(t *testing.T) {
	t.Setenv("LYCAON_CONFIG_DIR", t.TempDir())
	base := t.TempDir()
	cRepo := filepath.Join(base, "shared")
	writeReleasePack(t, cRepo, "acme/shared", "1.0.0", nil)
	extpackstest.GitInitCommit(t, cRepo)
	extpackstest.GitTag(t, cRepo, "v1.0.0")
	writeReleasePack(t, cRepo, "acme/shared", "2.0.0", nil)
	extpackstest.GitCommitAll(t, cRepo, "shared v2")
	extpackstest.GitTag(t, cRepo, "v2.0.0")

	aRepo := filepath.Join(base, "root-a")
	writeReleasePack(t, aRepo, "acme/root-a", "1.0.0", deps("acme/shared", cRepo, "^1.0.0"))
	extpackstest.GitInitCommit(t, aRepo)
	extpackstest.GitTag(t, aRepo, "v1.0.0")
	bRepo := filepath.Join(base, "root-b")
	writeReleasePack(t, bRepo, "acme/root-b", "1.0.0", deps("acme/shared", cRepo, "*"))
	extpackstest.GitInitCommit(t, bRepo)
	extpackstest.GitTag(t, bRepo, "v1.0.0")

	installDevice(t, "file://"+aRepo, "*")
	installDevice(t, "file://"+bRepo, "*")

	lockPath, err := extpacks.DeviceLockPath()
	testutil.FailErr(t, "DeviceLockPath", err)
	lock, err := extpacks.LoadLockFile(lockPath)
	testutil.FailErr(t, "LoadLockFile", err)
	shared, ok := lock.Package("acme/shared")
	if !ok || shared.Version != "1.0.0" {
		t.Fatalf("shared package = %+v, present=%v; existing root requires 1.x", shared, ok)
	}
}

func TestLockedReleaseRefusesTamperedContent(t *testing.T) {
	t.Setenv("LYCAON_CONFIG_DIR", t.TempDir())
	repo := filepath.Join(t.TempDir(), "tamper")
	writeReleasePack(t, repo, "acme/tamper", "1.0.0", nil)
	extpackstest.GitInitCommit(t, repo)
	extpackstest.GitTag(t, repo, "v1.0.0")
	result := installDevice(t, "file://"+repo, "1.0.0")
	testutil.FailErr(t, "tamper cached body", os.WriteFile(
		filepath.Join(result.PackageRoot, "policy", "ACME_HELLO.yaml"), []byte("id: CHANGED\n"), 0o600,
	))
	found, err := extpacks.DiscoverLockedContent(nil)
	testutil.FailErr(t, "DiscoverLockedContent", err)
	tampered := findPack(t, found, "acme/tamper")
	if tampered.OmitReason != extpacks.BlockedIntegrity {
		t.Fatalf("tampered omit = %q want integrity", tampered.OmitReason)
	}
	if len(tampered.Units) != 0 {
		t.Fatalf("tampered pack must not contribute units: %d", len(tampered.Units))
	}
}

func TestImmutableRevisionBodyIsReusableAcrossGitMirrors(t *testing.T) {
	t.Setenv("LYCAON_CONFIG_DIR", t.TempDir())
	base := t.TempDir()
	origin := filepath.Join(base, "origin")
	mirror := filepath.Join(base, "mirror")
	writeReleasePack(t, origin, "acme/mirrored", "1.0.0", nil)
	extpackstest.GitInitCommit(t, origin)
	extpackstest.GitTag(t, origin, "v1.0.0")
	clone := exec.Command("git", "clone", "--quiet", "--", origin, mirror)
	if out, err := clone.CombinedOutput(); err != nil {
		t.Fatalf("clone mirror: %v: %s", err, out)
	}

	first := installDevice(t, "file://"+origin, "1.0.0")
	before, err := os.ReadFile(filepath.Join(first.PackageRoot, extpacks.PackageBodyMetadataName))
	testutil.FailErr(t, "read body metadata", err)
	second := installDevice(t, "file://"+mirror, "1.0.0")
	if second.PackageRoot != first.PackageRoot {
		t.Fatalf("mirror body = %s, want shared immutable body %s", second.PackageRoot, first.PackageRoot)
	}
	after, err := os.ReadFile(filepath.Join(second.PackageRoot, extpacks.PackageBodyMetadataName))
	testutil.FailErr(t, "read reused body metadata", err)
	if string(after) != string(before) {
		t.Fatalf("reusing immutable body rewrote metadata\nbefore: %s\nafter: %s", before, after)
	}
	if _, err := extpacks.DiscoverLockedContent(nil); err != nil {
		t.Fatalf("discover mirrored lock against reused body: %v", err)
	}
}

func TestCachedBodyMetadataRejectsUnknownFields(t *testing.T) {
	t.Setenv("LYCAON_CONFIG_DIR", t.TempDir())
	repo := filepath.Join(t.TempDir(), "metadata")
	writeReleasePack(t, repo, "acme/metadata", "1.0.0", nil)
	extpackstest.GitInitCommit(t, repo)
	extpackstest.GitTag(t, repo, "v1.0.0")
	installed := installDevice(t, "file://"+repo, "1.0.0")
	metaPath := filepath.Join(installed.PackageRoot, extpacks.PackageBodyMetadataName)
	data, err := os.ReadFile(metaPath)
	testutil.FailErr(t, "read metadata", err)
	data = []byte(strings.TrimSuffix(strings.TrimSpace(string(data)), "}") + ",\n  \"unexpected\": true\n}\n")
	testutil.FailErr(t, "write unknown metadata field", os.WriteFile(metaPath, data, 0o600))
	found, err := extpacks.DiscoverLockedContent(nil)
	testutil.FailErr(t, "DiscoverLockedContent", err)
	corrupt := findPack(t, found, "acme/metadata")
	if corrupt.OmitReason != extpacks.BlockedIntegrity {
		t.Fatalf("corrupt metadata omit = %q want integrity", corrupt.OmitReason)
	}
	if len(corrupt.Units) != 0 {
		t.Fatalf("corrupt metadata pack must not contribute units: %d", len(corrupt.Units))
	}
	if !strings.Contains(corrupt.OmitDetail, "unknown field") {
		t.Fatalf("omit detail = %q, want unknown field", corrupt.OmitDetail)
	}
}

func TestLoadDesiredRejectsUnsupportedFormat(t *testing.T) {
	t.Setenv("LYCAON_CONFIG_DIR", t.TempDir())
	desiredPath, err := extpacks.DeviceDesiredPath()
	testutil.FailErr(t, "DeviceDesiredPath", err)
	data := "format: 2\npacks: []\ndisabled: []\nown: {}\n"
	testutil.FailErr(t, "write unsupported desired format", os.WriteFile(desiredPath, []byte(data), 0o600))
	_, err = extpacks.LoadDesiredFile(desiredPath)
	var formatErr *extpacks.UnsupportedFormatError
	if !errors.As(err, &formatErr) {
		t.Fatalf("LoadDesiredFile error = %v, want UnsupportedFormatError", err)
	}
	if formatErr.Doc != "extensions.yaml" || formatErr.Field != "format" || formatErr.Got != 2 || formatErr.Want != 1 {
		t.Fatalf("UnsupportedFormatError = %+v", formatErr)
	}
}

func TestParseManifestRejectsUnsupportedManifestVersion(t *testing.T) {
	data := []byte("manifest_version: 0\nid: acme/unsupported-format\nname: Unsupported format\nversion: 1.0.0\ncompatibility:\n  extension_api: ^1.0.0\n")
	_, err := extpacks.ParseManifest("acme/unsupported-format", data)
	var formatErr *extpacks.UnsupportedFormatError
	if !errors.As(err, &formatErr) {
		t.Fatalf("ParseManifest error = %v, want UnsupportedFormatError", err)
	}
	if formatErr.Doc != "extension.yaml" || formatErr.Field != "manifest_version" {
		t.Fatalf("UnsupportedFormatError = %+v", formatErr)
	}
}

func TestParseManifestRejectsUnknownFields(t *testing.T) {
	data := []byte("manifest_version: 1\nid: acme/a\nname: A\nversion: 1.0.0\ncompatibility:\n  extension_api: ^1.0.0\nreplaces: [acme/b]\n")
	if _, err := extpacks.ParseManifest("acme/a", data); err == nil || !strings.Contains(err.Error(), "replaces") {
		t.Fatalf("ParseManifest error = %v, want unknown-field rejection", err)
	}
}

func TestLockedReleaseRejectsStaleIntent(t *testing.T) {
	t.Setenv("LYCAON_CONFIG_DIR", t.TempDir())
	repo := filepath.Join(t.TempDir(), "stale-intent")
	writeReleasePack(t, repo, "acme/stale", "1.0.0", nil)
	extpackstest.GitInitCommit(t, repo)
	extpackstest.GitTag(t, repo, "v1.0.0")
	installDevice(t, "file://"+repo, "^1.0.0")
	desiredPath, err := extpacks.DeviceDesiredPath()
	testutil.FailErr(t, "DeviceDesiredPath", err)
	desired, err := extpacks.LoadDesiredFile(desiredPath)
	testutil.FailErr(t, "LoadDesiredFile", err)
	row, ok := extpacks.DesiredPackRow(desired, "acme/stale")
	if !ok {
		t.Fatal("desired row missing")
	}
	row.Version = "^2.0.0"
	desired.Packs = []extpacks.DesiredPack{row}
	writeDesiredForTest(t, desiredPath, desired)
	if _, err := extpacks.DiscoverLockedContent(nil); err == nil || !strings.Contains(err.Error(), "does not admit locked version") {
		t.Fatalf("stale desired/lock error = %v", err)
	}
}

func TestValidateLockRequiresExactDependencyVersion(t *testing.T) {
	lock := extpacks.LockFile{
		LockFormat: extpacks.LockFormat,
		Packages: []extpacks.LockedPackage{
			{ID: "acme/root", Version: "1.0.0", Source: "path:/root", Integrity: "sha256:a", Kind: extpacks.PackKindPath, Dependencies: map[string]string{"acme/dep": "1.0.0"}},
			{ID: "acme/dep", Version: "2.0.0", Source: "path:/dep", Integrity: "sha256:b", Kind: extpacks.PackKindPath},
		},
	}
	if err := extpacks.ValidateLock(lock); err == nil || !strings.Contains(err.Error(), "package entry is 2.0.0") {
		t.Fatalf("ValidateLock error = %v", err)
	}
}

func TestLoadLockRejectsRedundantResolutionFields(t *testing.T) {
	for _, field := range []string{"extension_api", "development"} {
		t.Run(field, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "extensions.lock.yaml")
			data := "lock_format: 1\n" + field + ": true\npackages: []\n"
			testutil.FailErr(t, "write lock", os.WriteFile(path, []byte(data), 0o600))
			if _, err := extpacks.LoadLockFile(path); err == nil || !strings.Contains(err.Error(), "field "+field+" not found") {
				t.Fatalf("LoadLockFile error = %v", err)
			}
		})
	}
}

func TestDurableResolutionCannotReplaceStockPackageIdentity(t *testing.T) {
	desired := extpacks.EmptyDesired()
	desired.Packs = []extpacks.DesiredPack{{
		ID: "painted-wolf/platform", Source: "https://example.com/platform.git", Version: "*",
	}}
	if err := extpacks.ValidateDesired(desired); err == nil || !strings.Contains(err.Error(), "cannot declare an external source") {
		t.Fatalf("ValidateDesired error = %v", err)
	}

	lock := extpacks.EmptyLock()
	lock.Packages = []extpacks.LockedPackage{{
		ID: "painted-wolf/platform", Version: "1.0.0", Source: "https://example.com/platform.git",
		Revision: "abc", Integrity: "sha256:abc", Kind: extpacks.PackKindGit,
	}}
	if err := extpacks.ValidateLock(lock); err == nil || !strings.Contains(err.Error(), "must not appear") {
		t.Fatalf("ValidateLock error = %v", err)
	}
}

func deps(id, repo, constraint string) map[string]extpacks.DependencyRequest {
	return map[string]extpacks.DependencyRequest{id: {Source: "file://" + repo, Version: constraint}}
}

func writeReleasePack(t *testing.T, dir, id, version string, dependencies map[string]extpacks.DependencyRequest) {
	t.Helper()
	manifest := extpacks.Manifest{
		ManifestVersion: extpacks.ManifestVersion, ID: id, Name: id, Version: version,
		Compatibility: extpacks.ManifestCompatibility{ExtensionAPI: "^1.0.0"},
		Dependencies:  dependencies,
	}
	data, err := yaml.Marshal(manifest)
	testutil.FailErr(t, "marshal manifest", err)
	testutil.FailErr(t, "create pack", os.MkdirAll(filepath.Join(dir, "policy"), 0o755))
	testutil.FailErr(t, "write manifest", os.WriteFile(filepath.Join(dir, "extension.yaml"), data, 0o644))
	testutil.FailErr(t, "write policy", os.WriteFile(
		filepath.Join(dir, "policy", "ACME_HELLO.yaml"), []byte("id: ACME_HELLO\nemit: banner\nmessage: hi\n"), 0o644,
	))
}
