package extensionstate_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/extensionstate"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/extpackstest"
)

func writeSuiteLeaf(t *testing.T, suite, leaf, id string) {
	t.Helper()
	dir := suite
	if leaf != "." {
		dir = filepath.Join(suite, leaf)
	}
	extpackstest.MustWrite(t, filepath.Join(dir, "extension.yaml"),
		"manifest_version: 1\nid: "+id+"\nname: "+id+"\nversion: 1.0.0\ncompatibility:\n  extension_api: \"^1.0.0\"\n")
	if leaf != "." {
		extpackstest.MustWrite(t, filepath.Join(dir, "guidance", leaf+".md"), "# "+leaf+"\n")
	}
}

func writeSuiteMeta(t *testing.T, suite, id string, members []string) {
	t.Helper()
	var b strings.Builder
	b.WriteString("manifest_version: 1\nid: " + id + "\nname: Kit\nversion: \"1.0.0\"\n")
	b.WriteString("compatibility:\n  extension_api: \"^1.0.0\"\nmembers:\n")
	for _, m := range members {
		b.WriteString("  - " + m + "\n")
	}
	b.WriteString("conflicts_with: []\nextends: []\n")
	extpackstest.MustWrite(t, filepath.Join(suite, extpacks.MetaPackFileName), b.String())
}

func TestInstallMetaPackPathMultiLeaf(t *testing.T) {
	extpacks.ClearActive()
	t.Cleanup(extpacks.ClearActive)
	owner := testOwner(t)
	suite := t.TempDir()
	writeSuiteLeaf(t, suite, "leaf-a", "acme/leaf-a")
	writeSuiteLeaf(t, suite, "leaf-b", "acme/leaf-b")
	writeSuiteMeta(t, suite, "acme/kit", []string{"acme/leaf-a", "acme/leaf-b"})

	res := apply(t, owner, deviceScope(), extensionstate.InstallMetaOp{Source: "path:" + suite})
	if res.Meta == nil || res.Meta.MetaPackID != "acme/kit" {
		t.Fatalf("meta result=%+v", res.Meta)
	}
	if len(res.Meta.NewlyInstalled) != 2 {
		t.Fatalf("newly=%v", res.Meta.NewlyInstalled)
	}
	if active := extpacks.Active(); active == nil || !active.PackContributed("acme/leaf-a") || !active.PackContributed("acme/leaf-b") {
		t.Fatal("device suite install must publish every member into the live catalog")
	}
	metas, _, err := extpacks.DiscoverMetaPacks()
	testutil.FailErr(t, "DiscoverMetaPacks", err)
	found := false
	for _, m := range metas {
		if m.Manifest.ID == "acme/kit" {
			found = true
			if m.Kind != extpacks.PackKindPath {
				t.Fatalf("meta kind=%s", m.Kind)
			}
		}
	}
	if !found {
		t.Fatal("acme/kit not discovered")
	}
	cached, err := extpacks.DiscoverLockedContent(nil)
	testutil.FailErr(t, "DiscoverLockedContent", err)
	ids := map[string]extpacks.PackKind{}
	for _, pc := range cached {
		ids[pc.Pack.ID] = pc.Kind
	}
	if ids["acme/leaf-a"] != extpacks.PackKindPath || ids["acme/leaf-b"] != extpacks.PackKindPath {
		t.Fatalf("want path-linked members, got %v", ids)
	}
}

func TestProjectMetaPackMembersCanBeDisabledAndEnabled(t *testing.T) {
	owner := testOwner(t)
	suite := t.TempDir()
	writeSuiteLeaf(t, suite, "leaf-a", "acme/leaf-a")
	writeSuiteMeta(t, suite, "acme/kit", []string{"acme/leaf-a"})

	apply(t, owner, deviceScope(), extensionstate.InstallMetaOp{Source: "path:" + suite})
	disabled := apply(t, owner, deviceScope(),
		extensionstate.ApplyMetaOp{MetaPackID: "acme/kit", Enable: false})
	if extpacks.PackEnabled(disabled.Desired, "acme/leaf-a") || len(disabled.Warnings) != 0 {
		t.Fatalf("disabled=%+v warnings=%v", disabled.Desired.Packs, disabled.Warnings)
	}
	enabled := apply(t, owner, deviceScope(),
		extensionstate.ApplyMetaOp{MetaPackID: "acme/kit", Enable: true})
	if !extpacks.PackEnabled(enabled.Desired, "acme/leaf-a") {
		t.Fatalf("enabled=%+v", enabled.Desired.Packs)
	}
	report := validateScope(t, "")
	found := false
	for _, meta := range report.MetaPacks {
		if meta.ID == "acme/kit" {
			found = true
			if meta.Status != extpacks.MetaPackComplete {
				t.Fatalf("project suite status=%s diagnostics=%v", meta.Status, meta.Diagnostics)
			}
		}
	}
	if !found {
		t.Fatal("acme/kit missing from project validation")
	}
}

func TestInstallMetaPackRefusesManagedCacheSource(t *testing.T) {
	owner := testOwner(t)
	suite, err := extpacks.CachedMetaPackDir("acme/kit")
	testutil.FailErr(t, "CachedMetaPackDir", err)
	writeSuiteLeaf(t, suite, "leaf-a", "acme/leaf-a")
	writeSuiteMeta(t, suite, "acme/kit", []string{"acme/leaf-a"})

	_, err = tryApply(t, owner, deviceScope(), extensionstate.InstallMetaOp{Source: "path:" + suite})
	if err == nil || !strings.Contains(err.Error(), "extensions meta-pack cache") {
		t.Fatalf("err=%v; managed cache source must be refused", err)
	}
	if _, err := os.Stat(filepath.Join(suite, "leaf-a", "extension.yaml")); err != nil {
		t.Fatalf("suite source was modified: %v", err)
	}
}

func TestInstallMetaPackConcurrentReplacement(t *testing.T) {
	owner := testOwner(t)
	suite := t.TempDir()
	writeSuiteLeaf(t, suite, "leaf-a", "acme/leaf-a")
	writeSuiteMeta(t, suite, "acme/kit", []string{"acme/leaf-a"})

	const installs = 8
	var wg sync.WaitGroup
	errs := make(chan error, installs)
	for range installs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := applyWithRetry(t, owner, deviceScope(),
				extensionstate.InstallMetaOp{Source: "path:" + suite})
			if err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		testutil.FailErr(t, "concurrent meta install", err)
	}

	metas, diags, err := extpacks.DiscoverMetaPacks()
	testutil.FailErr(t, "DiscoverMetaPacks", err)
	for _, diag := range diags {
		if diag.PackID == "acme/kit" {
			t.Fatalf("suite cache diagnostic: %+v", diag)
		}
	}
	found := false
	for _, meta := range metas {
		found = found || meta.Manifest.ID == "acme/kit"
	}
	if !found {
		t.Fatal("acme/kit not discovered")
	}
}

func TestInstallMetaAmbiguousBothManifests(t *testing.T) {
	owner := testOwner(t)
	suite := t.TempDir()
	writeSuiteLeaf(t, suite, ".", "acme/both")
	writeSuiteMeta(t, suite, "acme/both-meta", []string{"acme/x"})
	_, err := tryApply(t, owner, deviceScope(), extensionstate.InstallMetaOp{Source: "path:" + suite})
	if !errors.Is(err, extpacks.ErrAmbiguousPackRoot) {
		t.Fatalf("want ErrAmbiguousPackRoot, got %v", err)
	}
}

func TestPackInstallMetaOnlyRoot(t *testing.T) {
	owner := testOwner(t)
	suite := t.TempDir()
	writeSuiteMeta(t, suite, "acme/kit", []string{"acme/x"})
	_, err := tryApply(t, owner, deviceScope(), extensionstate.InstallOp{Source: "path:" + suite})
	if !errors.Is(err, extpacks.ErrUseInstallMeta) {
		t.Fatalf("want ErrUseInstallMeta, got %v", err)
	}
}

func TestRemoveMetaPackKeepsInstalledMembers(t *testing.T) {
	owner := testOwner(t)
	suite := t.TempDir()
	writeSuiteLeaf(t, suite, "leaf-a", "acme/leaf-a")
	writeSuiteMeta(t, suite, "acme/kit", []string{"acme/leaf-a"})
	apply(t, owner, deviceScope(), extensionstate.InstallMetaOp{Source: "path:" + suite})

	apply(t, owner, deviceScope(), extensionstate.RemoveMetaOp{MetaPackID: "acme/kit"})
	metaDir, err := extpacks.CachedMetaPackDir("acme/kit")
	testutil.FailErr(t, "CachedMetaPackDir", err)
	if _, err := os.Stat(metaDir); !os.IsNotExist(err) {
		t.Fatal("meta dir should be gone")
	}
	locked, err := extpacks.DiscoverLockedContent(nil)
	testutil.FailErr(t, "DiscoverLockedContent", err)
	if len(locked) != 1 || locked[0].Pack.ID != "acme/leaf-a" {
		t.Fatalf("removing suite grouping removed installed member: %+v", locked)
	}
}

func TestRemoveMetaPackRejectsStaleRevisionWithoutDeletingSuite(t *testing.T) {
	owner := testOwner(t)
	suite := t.TempDir()
	writeSuiteLeaf(t, suite, "leaf-a", "acme/leaf-a")
	writeSuiteMeta(t, suite, "acme/kit", []string{"acme/leaf-a"})
	apply(t, owner, deviceScope(), extensionstate.InstallMetaOp{Source: "path:" + suite})
	stale := currentRevision(t, owner, "")
	apply(t, owner, deviceScope(), extensionstate.SetUnitDisabledOp{UnitID: stockUnitIDAt(t, 0), Disabled: true})

	_, err := owner.Apply(t.Context(), extensionstate.Intent{
		Scope:            deviceScope(),
		ExpectedRevision: stale,
		Op:               extensionstate.RemoveMetaOp{MetaPackID: "acme/kit"},
	})
	var staleErr *extensionstate.StaleError
	if !errors.As(err, &staleErr) {
		t.Fatalf("remove meta error = %v, want stale revision", err)
	}
	metaDir, pathErr := extpacks.CachedMetaPackDir("acme/kit")
	testutil.FailErr(t, "CachedMetaPackDir", pathErr)
	if _, statErr := os.Stat(metaDir); statErr != nil {
		t.Fatalf("stale removal changed suite cache: %v", statErr)
	}
}
