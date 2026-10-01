package extensionstate_test

import (
	"context"
	"errors"
	gittestsetup "github.com/lycaon/lycaon/internal/testsetup/git"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/catalogview"
	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/extensionstate"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/extpackstest"
)

func TestMain(m *testing.M) {
	gittestsetup.Enable()
	os.Exit(m.Run())
}

func projectScope(projectDir string) extensionstate.Scope {
	return extensionstate.Scope{Kind: "project", ProjectID: projectDir, ProjectDir: projectDir}
}

func deviceScope() extensionstate.Scope { return extensionstate.Scope{Kind: "device"} }

func mustDeviceDesiredPath(t *testing.T) string {
	t.Helper()
	path, err := extpacks.DeviceDesiredPath()
	testutil.FailErr(t, "device desired path", err)
	return path
}

func mustDeviceLockPath(t *testing.T) string {
	t.Helper()
	path, err := extpacks.DeviceLockPath()
	testutil.FailErr(t, "device lock path", err)
	return path
}

func tryApply(t *testing.T, owner *extensionstate.Owner, scope extensionstate.Scope, op extensionstate.Op) (extensionstate.Result, error) {
	t.Helper()
	return owner.Apply(t.Context(), extensionstate.Intent{
		Scope:            scope,
		ExpectedRevision: currentRevision(t, owner, scope.ProjectDir),
		Op:               op,
	})
}

// applyWithRetry retries optimistic staleness until commit.
func applyWithRetry(t *testing.T, owner *extensionstate.Owner, scope extensionstate.Scope, op extensionstate.Op) error {
	t.Helper()
	for {
		revision, err := owner.CurrentRevision(scope.ProjectDir)
		if err != nil {
			return err
		}
		_, err = owner.Apply(t.Context(), extensionstate.Intent{
			Scope:            scope,
			ExpectedRevision: revision,
			Op:               op,
		})
		var stale *extensionstate.StaleError
		if errors.As(err, &stale) {
			continue
		}
		return err
	}
}

func TestInstallPathAndValidate(t *testing.T) {
	owner := testOwner(t)
	fixture := filepath.Join(t.TempDir(), "acme-pack")
	extpackstest.WriteMinimalPack(t, fixture, "acme/test-pack", 1)

	res := apply(t, owner, deviceScope(), extensionstate.InstallOp{Source: "path:" + fixture})
	if res.Install == nil || res.Install.PackID != "acme/test-pack" {
		t.Fatalf("install resolution = %+v", res.Install)
	}
	if res.Install.Kind != extpacks.PackKindPath {
		t.Fatalf("kind=%s want path", res.Install.Kind)
	}
	if res.PackageRoot != fixture {
		t.Fatalf("linked install root = %q, want author root %q", res.PackageRoot, fixture)
	}
	if _, err := os.Stat(filepath.Join(res.PackageRoot, extpacks.PackageBodyMetadataName)); !os.IsNotExist(err) {
		t.Fatalf("linked install must not write metadata into the author tree: %v", err)
	}
	lock, err := extpacks.LoadLockFile(mustDeviceLockPath(t))
	testutil.FailErr(t, "load device lock", err)
	locked, ok := lock.Package("acme/test-pack")
	if !ok || locked.Kind != extpacks.PackKindPath || locked.Integrity == "" {
		t.Fatalf("development lock entry = %+v, present=%v", locked, ok)
	}
	desired, err := extpacks.LoadDesiredFile(mustDeviceDesiredPath(t))
	testutil.FailErr(t, "extpacks.LoadDesiredFile failed", err)
	found := false
	for _, p := range desired.Packs {
		if p.ID == "acme/test-pack" && p.Enabled != nil && *p.Enabled {
			found = true
		}
	}
	if !found {
		t.Fatalf("desired missing enabled pack: %+v", desired.Packs)
	}

	rep := validateScope(t, "")
	if !rep.OK {
		t.Fatalf("validate should OK without conflicts:\n%s", extpacks.FormatValidateText(rep))
	}
	if !hasPack(rep, "acme/test-pack") {
		t.Fatal("validate missing installed pack")
	}
}

func TestInstallBindsReviewedSuggestionIDToResolvedPackage(t *testing.T) {
	owner := testOwner(t)
	fixture := filepath.Join(t.TempDir(), "actual-pack")
	extpackstest.WriteMinimalPack(t, fixture, "acme/actual", 1)
	before := currentRevision(t, owner, "")

	_, err := tryApply(t, owner, deviceScope(), extensionstate.InstallOp{
		Source: "path:" + fixture, ExpectedPackID: "acme/reviewed",
	})
	if err == nil || !strings.Contains(err.Error(), "resolves to package acme/actual") {
		t.Fatalf("mismatched reviewed id error = %v", err)
	}
	if after := currentRevision(t, owner, ""); after != before {
		t.Fatalf("mismatched reviewed id changed extension state: before=%s after=%s", before, after)
	}
}

// Linked dependencies use the same local-source resolution as root installs.
func TestInstallResolvesPathDependency(t *testing.T) {
	owner := testOwner(t)

	target := filepath.Join(t.TempDir(), "dep-target")
	extpackstest.WriteMinimalPack(t, target, "acme/dep-target", 1)

	dependent := filepath.Join(t.TempDir(), "dependent")
	extpackstest.MustWrite(t, filepath.Join(dependent, "extension.yaml"),
		"manifest_version: 1\nid: acme/dependent\nname: dependent\nversion: 1.0.0\n"+
			"compatibility:\n  extension_api: \"^1.0.0\"\ndependencies:\n"+
			"  painted-wolf/platform:\n    version: \"^1.0.0\"\n"+
			"  acme/dep-target:\n    source: \"path:"+target+"\"\n    version: \"^1.0.0\"\n")
	// The dependency has a distinct policy ID.
	extpackstest.MustWrite(t, filepath.Join(dependent, "policy", "ACME_DEPENDENT.yaml"),
		"id: ACME_DEPENDENT\nemit: banner\nmessage: hi\neffect: warn\n")

	res := apply(t, owner, deviceScope(), extensionstate.InstallOp{Source: "path:" + dependent})
	if res.Install == nil || res.Install.PackID != "acme/dependent" {
		t.Fatalf("install resolution = %+v", res.Install)
	}
	lock, err := extpacks.LoadLockFile(mustDeviceLockPath(t))
	testutil.FailErr(t, "load device lock", err)
	depLocked, ok := lock.Package("acme/dep-target")
	if !ok || depLocked.Kind != extpacks.PackKindPath {
		t.Fatalf("dependency lock entry = %+v, present=%v, want PackKindPath", depLocked, ok)
	}
	rep := validateScope(t, "")
	if !rep.OK {
		t.Fatalf("validate should OK a resolved path dependency:\n%s", extpacks.FormatValidateText(rep))
	}
	if !hasPack(rep, "acme/dep-target") || !hasPack(rep, "acme/dependent") {
		t.Fatalf("validate missing dependency or dependent:\n%s", extpacks.FormatValidateText(rep))
	}

	// Transitive dependencies are removed with their dependents.
	if _, err := tryApply(t, owner, deviceScope(), extensionstate.RemoveOp{PackID: "acme/dep-target"}); err == nil {
		t.Fatal("direct removal of a transitive path dependency must be refused")
	}
	// Removing the dependent preserves the author's linked directory.
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("dependency author directory must survive resolution: %v", err)
	}
	apply(t, owner, deviceScope(), extensionstate.RemoveOp{PackID: "acme/dependent"})
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("dependency author directory must survive removal too: %v", err)
	}
	lock, err = extpacks.LoadLockFile(mustDeviceLockPath(t))
	testutil.FailErr(t, "reload device lock", err)
	if _, ok := lock.Package("acme/dep-target"); ok {
		t.Fatal("orphaned transitive dependency must be garbage-collected on removal")
	}
}

func TestInstallFileGitClone(t *testing.T) {
	owner := testOwner(t)
	repo := filepath.Join(t.TempDir(), "git-pack")
	extpackstest.WriteMinimalPack(t, repo, "acme/git-pack", 1)
	extpackstest.GitInitCommit(t, repo)

	res := apply(t, owner, deviceScope(),
		extensionstate.InstallOp{Source: "file://" + repo, Ref: "HEAD"})
	if res.Install == nil || res.Install.Kind != extpacks.PackKindGit {
		t.Fatalf("install resolution = %+v, want git kind", res.Install)
	}
	if res.Install.ResolvedRevision == "" {
		t.Fatal("expected resolved revision")
	}
}

// An unchanged root can resolve a changed dependency.
func TestMutationReportsThePackagesItPublished(t *testing.T) {
	owner := testOwner(t)
	base := filepath.Join(t.TempDir(), "base-pack")
	extpackstest.WriteMinimalPack(t, base, "acme/base", 1)
	extpackstest.GitInitCommit(t, base)

	res := apply(t, owner, deviceScope(),
		extensionstate.InstallOp{Source: "file://" + base, Ref: "HEAD"})
	if len(res.PackageChanges) == 0 {
		t.Fatal("an install that published a package reported no package changes")
	}
	found := false
	for _, c := range res.PackageChanges {
		if c.PackID == "acme/base" {
			found = true
			if c.Kind != "added" {
				t.Fatalf("first install of acme/base = %q, want added", c.Kind)
			}
		}
	}
	if !found {
		t.Fatalf("package changes never mention the installed pack: %+v", res.PackageChanges)
	}
}

func TestProjectRejectsPackInstall(t *testing.T) {
	owner := testOwner(t)
	devicePack := filepath.Join(t.TempDir(), "device-pack")
	projectPack := filepath.Join(t.TempDir(), "project-pack")
	extpackstest.WriteMinimalPack(t, devicePack, "acme/shared-pack", 1)
	extpackstest.WriteMinimalPack(t, projectPack, "acme/shared-pack", 1)

	apply(t, owner, deviceScope(), extensionstate.InstallOp{Source: "path:" + devicePack})

	_, err := tryApply(t, owner, projectScope(t.TempDir()),
		extensionstate.InstallOp{Source: "path:" + projectPack})
	if err == nil || !errors.Is(err, extensionstate.ErrProjectMutationUnsupported) {
		t.Fatalf("project install error = %v, want ErrProjectMutationUnsupported", err)
	}
}

func TestProjectRejectsConfiguration(t *testing.T) {
	owner := testOwner(t)
	_, err := tryApply(t, owner, projectScope(t.TempDir()), extensionstate.SetConfigurationOp{
		Packs: map[string]map[string]any{"acme/reviewer": {"review_depth": "normal"}},
	})
	if err == nil || !errors.Is(err, extensionstate.ErrProjectMutationUnsupported) {
		t.Fatalf("project configuration error = %v, want ErrProjectMutationUnsupported", err)
	}
}

func TestUpdateRejectsManifestIdentityChangeBeforePublishing(t *testing.T) {
	owner := testOwner(t)
	repo := filepath.Join(t.TempDir(), "git-pack")
	extpackstest.WriteMinimalPack(t, repo, "acme/git-pack", 1)
	extpackstest.GitInitCommit(t, repo)
	installed := apply(t, owner, deviceScope(),
		extensionstate.InstallOp{Source: "file://" + repo, Ref: "HEAD"})

	extpackstest.WriteMinimalPack(t, repo, "acme/renamed-pack", 1)
	extpackstest.GitCommitAll(t, repo, "rename manifest identity")
	_, err := tryApply(t, owner, deviceScope(), extensionstate.UpdateOp{PackID: "acme/git-pack"})
	if err == nil || !strings.Contains(err.Error(), "declares acme/renamed-pack") {
		t.Fatalf("update error = %v, want identity validation", err)
	}
	manifest, err := os.ReadFile(filepath.Join(installed.PackageRoot, "extension.yaml"))
	testutil.FailErr(t, "read active manifest", err)
	if !strings.Contains(string(manifest), "id: acme/git-pack") {
		t.Fatalf("invalid update changed the selected body: %s", manifest)
	}
	meta, err := extpacks.ReadPackageBodyMetadata(installed.PackageRoot)
	testutil.FailErr(t, "read active metadata", err)
	if meta.ResolvedRevision != installed.Install.ResolvedRevision {
		t.Fatalf("selected revision = %q, want %q", meta.ResolvedRevision, installed.Install.ResolvedRevision)
	}
}

func TestUpdateRejectsInvalidCandidateAndPreservesBody(t *testing.T) {
	owner := testOwner(t)
	cfgDir := os.Getenv("LYCAON_CONFIG_DIR")
	repo := filepath.Join(t.TempDir(), "git-pack")
	extpackstest.WriteMinimalPack(t, repo, "acme/git-pack", 1)
	extpackstest.GitInitCommit(t, repo)
	installed := apply(t, owner, deviceScope(),
		extensionstate.InstallOp{Source: "file://" + repo, Ref: "HEAD"})

	// A foreign namespace makes the candidate view invalid.
	extpackstest.MustWrite(t, filepath.Join(repo, "contributions", "commands", "steal.yaml"), "id: acme/other:steal\n")
	extpackstest.MustWrite(t, filepath.Join(repo, "policy", "ACME_HELLO.yaml"), "id: ACME_HELLO\nemit: banner\nmessage: replacement\n")
	extpackstest.GitCommitAll(t, repo, "replacement")
	_, err := tryApply(t, owner, deviceScope(), extensionstate.UpdateOp{PackID: "acme/git-pack"})
	if err == nil {
		t.Fatal("update to an invalid candidate must reject")
	}

	meta, err := extpacks.ReadPackageBodyMetadata(installed.PackageRoot)
	testutil.FailErr(t, "read restored metadata", err)
	if meta.ResolvedRevision != installed.Install.ResolvedRevision {
		t.Fatalf("restored revision = %q, want %q", meta.ResolvedRevision, installed.Install.ResolvedRevision)
	}
	policy, err := os.ReadFile(filepath.Join(installed.PackageRoot, "policy", "ACME_HELLO.yaml"))
	testutil.FailErr(t, "read restored policy", err)
	if strings.Contains(string(policy), "replacement") {
		t.Fatalf("rejected candidate remained active: %s", policy)
	}
	assertNoPackInstallTransactions(t, cfgDir)
}

func TestInstallFailurePreservesSelectedBody(t *testing.T) {
	owner := testOwner(t)
	cfgDir := os.Getenv("LYCAON_CONFIG_DIR")
	repo := filepath.Join(t.TempDir(), "git-pack")
	extpackstest.WriteMinimalPack(t, repo, "acme/git-pack", 1)
	extpackstest.GitInitCommit(t, repo)

	old := apply(t, owner, deviceScope(),
		extensionstate.InstallOp{Source: "file://" + repo, Ref: "HEAD"})
	extpackstest.MustWrite(t, filepath.Join(repo, "policy", "ACME_HELLO.yaml"), "id: ACME_HELLO\nemit: banner\nmessage: replacement\n")
	extpackstest.GitCommitAll(t, repo, "replacement")

	revision := currentRevision(t, owner, "")
	desiredPath := mustDeviceDesiredPath(t)
	desiredBackup := desiredPath + ".backup"
	testutil.FailErr(t, "move desired file aside", os.Rename(desiredPath, desiredBackup))
	testutil.FailErr(t, "block desired file path", os.Mkdir(desiredPath, 0o700))

	_, err := owner.Apply(t.Context(), extensionstate.Intent{
		Scope:            deviceScope(),
		ExpectedRevision: revision,
		Op:               extensionstate.InstallOp{Source: "file://" + repo, Ref: "HEAD"},
	})
	if err == nil {
		t.Fatal("reinstall with an unwritable desired path must fail")
	}

	meta, err := extpacks.ReadPackageBodyMetadata(old.PackageRoot)
	testutil.FailErr(t, "read restored pack meta", err)
	if meta.ResolvedRevision != old.Install.ResolvedRevision {
		t.Fatalf("selected revision = %q, want %q", meta.ResolvedRevision, old.Install.ResolvedRevision)
	}
	policy, err := os.ReadFile(filepath.Join(old.PackageRoot, "policy", "ACME_HELLO.yaml"))
	testutil.FailErr(t, "read restored pack body", err)
	if strings.Contains(string(policy), "replacement") {
		t.Fatalf("failed reinstall left replacement body in cache: %s", policy)
	}
	assertNoPackInstallTransactions(t, cfgDir)

	testutil.FailErr(t, "remove desired path blocker", os.Remove(desiredPath))
	testutil.FailErr(t, "restore desired file", os.Rename(desiredBackup, desiredPath))
	replacement := apply(t, owner, deviceScope(),
		extensionstate.InstallOp{Source: "file://" + repo, Ref: "HEAD"})
	if replacement.Install.ResolvedRevision == old.Install.ResolvedRevision {
		t.Fatal("successful reinstall kept original revision")
	}
	policy, err = os.ReadFile(filepath.Join(replacement.PackageRoot, "policy", "ACME_HELLO.yaml"))
	testutil.FailErr(t, "read replacement pack body", err)
	if !strings.Contains(string(policy), "replacement") {
		t.Fatalf("successful reinstall did not activate replacement body: %s", policy)
	}
	assertNoPackInstallTransactions(t, cfgDir)
}

func TestInstallFailureDoesNotPublishNewIntent(t *testing.T) {
	owner := testOwner(t)
	cfgDir := os.Getenv("LYCAON_CONFIG_DIR")
	repo := filepath.Join(t.TempDir(), "git-pack")
	extpackstest.WriteMinimalPack(t, repo, "acme/git-pack", 1)
	extpackstest.GitInitCommit(t, repo)

	revision := currentRevision(t, owner, "")
	desiredPath := mustDeviceDesiredPath(t)
	testutil.FailErr(t, "create desired parent", os.MkdirAll(filepath.Dir(desiredPath), 0o700))
	testutil.FailErr(t, "block desired file path", os.Mkdir(desiredPath, 0o700))
	_, err := owner.Apply(t.Context(), extensionstate.Intent{
		Scope:            deviceScope(),
		ExpectedRevision: revision,
		Op:               extensionstate.InstallOp{Source: "file://" + repo, Ref: "HEAD"},
	})
	if err == nil {
		t.Fatal("install with an unwritable desired path must fail")
	}
	if _, statErr := os.Stat(mustDeviceLockPath(t)); !os.IsNotExist(statErr) {
		t.Fatalf("failed install published a lock: %v", statErr)
	}
	assertNoPackInstallTransactions(t, cfgDir)
}

func TestInstallStagingFailurePreservesSelectedBody(t *testing.T) {
	owner := testOwner(t)
	cfgDir := os.Getenv("LYCAON_CONFIG_DIR")
	originalRepo := filepath.Join(t.TempDir(), "original")
	extpackstest.WriteMinimalPack(t, originalRepo, "acme/git-pack", 1)
	extpackstest.GitInitCommit(t, originalRepo)
	original := apply(t, owner, deviceScope(),
		extensionstate.InstallOp{Source: "file://" + originalRepo, Ref: "HEAD"})

	replacementRepo := filepath.Join(t.TempDir(), "replacement")
	extpackstest.WriteMinimalPack(t, replacementRepo, "acme/git-pack", 1)
	testutil.FailErr(t, "create dangling replacement symlink", os.Symlink("missing-target", filepath.Join(replacementRepo, "broken-link")))
	extpackstest.GitInitCommit(t, replacementRepo)
	_, err := tryApply(t, owner, deviceScope(),
		extensionstate.InstallOp{Source: "file://" + replacementRepo, Ref: "HEAD"})
	if err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("install error = %v, want integrity refusal", err)
	}

	meta, err := extpacks.ReadPackageBodyMetadata(original.PackageRoot)
	testutil.FailErr(t, "read preserved pack meta", err)
	if meta.ResolvedRevision != original.Install.ResolvedRevision {
		t.Fatalf("preserved revision = %q, want %q", meta.ResolvedRevision, original.Install.ResolvedRevision)
	}
	if _, err := os.Stat(filepath.Join(original.PackageRoot, "policy", "ACME_HELLO.yaml")); err != nil {
		testutil.FailErr(t, "stat preserved pack body", err)
	}
	assertNoPackInstallTransactions(t, cfgDir)
}

func TestInstallCanceledRequestAbortsCloneAndCleansStaging(t *testing.T) {
	owner := testOwner(t)
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	t.Setenv("TMP", tmp)
	t.Setenv("TEMP", tmp)

	repo := filepath.Join(t.TempDir(), "git-pack")
	extpackstest.WriteMinimalPack(t, repo, "acme/git-pack", 1)
	extpackstest.GitInitCommit(t, repo)

	revision := currentRevision(t, owner, "")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := owner.Apply(ctx, extensionstate.Intent{
		Scope:            deviceScope(),
		ExpectedRevision: revision,
		Op:               extensionstate.InstallOp{Source: "file://" + repo, Ref: "HEAD"},
	})
	if err == nil {
		t.Fatal("install under a canceled request context must fail, not run the clone to completion")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v; want it to wrap context.Canceled", err)
	}
	leaked, globErr := filepath.Glob(filepath.Join(tmp, "lycaon-pack-*"))
	if globErr != nil {
		t.Fatal(globErr)
	}
	if len(leaked) != 0 {
		t.Fatalf("staging checkouts left behind after cancellation: %v", leaked)
	}
}

// Command-executing transports are rejected before process execution.
func TestInstallRejectsCommandExecutingTransport(t *testing.T) {
	owner := testOwner(t)
	marker := filepath.Join(t.TempDir(), "pwned")

	for _, source := range []string{
		"ext::sh -c 'touch " + marker + "'",
		"fd::7",
		"--upload-pack=touch " + marker,
	} {
		_, err := tryApply(t, owner, deviceScope(), extensionstate.InstallOp{Source: source})
		if err == nil {
			t.Fatalf("install(source=%q) = nil error, want refusal", source)
		}
		if _, statErr := os.Stat(marker); statErr == nil {
			t.Fatalf("install(source=%q) executed the URL payload", source)
		}
	}
}

func TestInstallEpochMismatch(t *testing.T) {
	owner := testOwner(t)
	fixture := filepath.Join(t.TempDir(), "bad-epoch")
	extpackstest.WriteMinimalPack(t, fixture, "acme/bad-epoch", 99)
	_, err := tryApply(t, owner, deviceScope(), extensionstate.InstallOp{Source: "path:" + fixture})
	if err == nil || !strings.Contains(err.Error(), "extension API") {
		t.Fatalf("want extension API compatibility error, got %v", err)
	}
}

func TestValidateConflictExit(t *testing.T) {
	owner := testOwner(t)

	a := filepath.Join(t.TempDir(), "a")
	b := filepath.Join(t.TempDir(), "b")
	extpackstest.WriteMinimalPack(t, a, "acme/a", 1)
	extpackstest.WriteMinimalPack(t, b, "acme/b", 1)
	extpackstest.MustWrite(t, filepath.Join(a, "policy", "DUP_CODE.yaml"), "id: DUP_CODE\nemit: banner\nmessage: a\n")
	extpackstest.MustWrite(t, filepath.Join(b, "policy", "DUP_CODE.yaml"), "id: DUP_CODE\nemit: banner\nmessage: b\n")

	apply(t, owner, deviceScope(), extensionstate.InstallOp{Source: "path:" + a})
	apply(t, owner, deviceScope(), extensionstate.InstallOp{Source: "path:" + b})
	rep := validateScope(t, "")
	if rep.OK || rep.Conflicts < 1 {
		t.Fatalf("want conflict fail, got ok=%v conflicts=%d\n%s", rep.OK, rep.Conflicts, extpacks.FormatValidateText(rep))
	}
}

// An explicit `own:` entry resolves a tool-schema collision.
func TestInstallToolSchemaCollisionThenOwnResolves(t *testing.T) {
	owner := testOwner(t)

	voice := filepath.Join(t.TempDir(), "tool-voice")
	extpackstest.WriteMinimalPack(t, voice, "acme/tool-voice", 1)
	extpackstest.MustWrite(t, filepath.Join(voice, "tools", "schemas", "write.yaml"),
		"description: acme write\nschema:\n  type: object\n")

	apply(t, owner, deviceScope(), extensionstate.InstallOp{Source: "path:" + voice})
	rep := validateScope(t, "")
	if rep.OK {
		t.Fatal("want validate to flag the unresolved tools/schemas/write conflict")
	}

	packID := "acme/tool-voice"
	apply(t, owner, deviceScope(), extensionstate.SetUnitOwnOp{
		UnitID: "tools/schemas/write", PackID: &packID,
	})
	rep = validateScope(t, "")
	if !rep.OK {
		t.Fatalf("own must resolve the collision, got\n%s", extpacks.FormatValidateText(rep))
	}
}

func TestProfileApplyIdempotent(t *testing.T) {
	owner := testOwner(t)
	fixture := filepath.Join(t.TempDir(), "prof-pack")
	extpackstest.WriteMinimalPack(t, fixture, "acme/prof", 1)
	extpackstest.MustWrite(t, filepath.Join(fixture, "profiles", "quieter.yaml"),
		"name: quieter\ndisable:\n  - painted-wolf/options\nown:\n  policy/ACME_HELLO: acme/prof\n")

	apply(t, owner, deviceScope(), extensionstate.InstallOp{Source: "path:" + fixture})
	r1 := apply(t, owner, deviceScope(), extensionstate.ApplyProfileOp{PackID: "acme/prof", Profile: "quieter"})
	r2 := apply(t, owner, deviceScope(), extensionstate.ApplyProfileOp{PackID: "acme/prof", Profile: "quieter"})
	if len(r1.Desired.Packs) != len(r2.Desired.Packs) {
		t.Fatal("profile apply not idempotent on packs")
	}
	if r1.Desired.Own["policy/ACME_HELLO"] != "acme/prof" || r2.Desired.Own["policy/ACME_HELLO"] != "acme/prof" {
		t.Fatalf("own mismatch: %+v %+v", r1.Desired.Own, r2.Desired.Own)
	}
}

func TestRemoveClearsOwn(t *testing.T) {
	owner := testOwner(t)
	fixture := filepath.Join(t.TempDir(), "rm-pack")
	extpackstest.WriteMinimalPack(t, fixture, "acme/rm", 1)
	apply(t, owner, deviceScope(), extensionstate.InstallOp{Source: "path:" + fixture})
	pack := "acme/rm"
	apply(t, owner, deviceScope(), extensionstate.SetUnitOwnOp{UnitID: "policy/ACME_HELLO", PackID: &pack})
	apply(t, owner, deviceScope(), extensionstate.RemoveOp{PackID: "acme/rm"})
	d, err := extpacks.LoadDesiredFile(mustDeviceDesiredPath(t))
	testutil.FailErr(t, "extpacks.LoadDesiredFile failed", err)
	if _, ok := d.Own["policy/ACME_HELLO"]; ok {
		t.Fatal("own should be cleared on remove")
	}
}

func TestRemovePreservesIntentWhenDesiredWriteFails(t *testing.T) {
	owner := testOwner(t)
	fixture := filepath.Join(t.TempDir(), "rm-pack")
	extpackstest.WriteMinimalPack(t, fixture, "acme/rm", 1)
	apply(t, owner, deviceScope(), extensionstate.InstallOp{Source: "path:" + fixture})
	desiredBefore, err := os.ReadFile(mustDeviceDesiredPath(t))
	testutil.FailErr(t, "read desired before failed remove", err)
	lockBefore, err := os.ReadFile(mustDeviceLockPath(t))
	testutil.FailErr(t, "read lock before failed remove", err)

	revision := currentRevision(t, owner, "")
	desiredPath := mustDeviceDesiredPath(t)
	backupPath := desiredPath + ".backup"
	testutil.FailErr(t, "move desired state", os.Rename(desiredPath, backupPath))
	testutil.FailErr(t, "block desired state path", os.Mkdir(desiredPath, 0o700))
	_, err = owner.Apply(t.Context(), extensionstate.Intent{
		Scope:            deviceScope(),
		ExpectedRevision: revision,
		Op:               extensionstate.RemoveOp{PackID: "acme/rm"},
	})
	if err == nil {
		t.Fatal("remove with unwritable desired state succeeded")
	}
	desiredAfter, readErr := os.ReadFile(backupPath)
	testutil.FailErr(t, "read preserved desired", readErr)
	lockAfter, readErr := os.ReadFile(mustDeviceLockPath(t))
	testutil.FailErr(t, "read preserved lock", readErr)
	if string(desiredAfter) != string(desiredBefore) || string(lockAfter) != string(lockBefore) {
		t.Fatal("failed remove changed desired state or exact lock")
	}
	testutil.FailErr(t, "remove desired blocker", os.Remove(desiredPath))
	testutil.FailErr(t, "restore desired state", os.Rename(backupPath, desiredPath))
}

func assertNoPackInstallTransactions(t *testing.T, configDir string) {
	t.Helper()
	entries, err := os.ReadDir(configDir)
	testutil.FailErr(t, "read config directory", err)
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".extension-pack-install-") {
			t.Fatalf("extension pack transaction left behind: %s", entry.Name())
		}
	}
}

func hasPack(rep *extpacks.ValidateReport, id string) bool {
	for _, p := range rep.Packs {
		if p.ID == id {
			return true
		}
	}
	return false
}

func validateScope(t *testing.T, projectDir string) *extpacks.ValidateReport {
	t.Helper()
	eff, err := extpacks.ResolveCatalog(t.Context(), []string{projectDir}, nil)
	testutil.FailErr(t, "ResolveCatalog", err)
	views := catalogview.NewCache(configlayout.FindModuleRoot(), nil)
	rep, err := extpacks.Validate(t.Context(), projectDir, views.InForce(t.Context(), eff))
	testutil.FailErr(t, "extpacks.Validate failed", err)
	return rep
}
