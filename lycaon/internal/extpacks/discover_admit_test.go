package extpacks_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/extpackstest"
)

func TestLinkedPathChangeMarksNeedsReloadAndStillContributes(t *testing.T) {
	t.Setenv("LYCAON_CONFIG_DIR", t.TempDir())
	author := filepath.Join(t.TempDir(), "acme-pack")
	extpackstest.WriteMinimalPack(t, author, "acme/shadow", 1)
	installLinked(t, author)

	testutil.FailErr(t, "edit linked pack", os.WriteFile(
		filepath.Join(author, "policy", "ACME_HELLO.yaml"),
		[]byte("id: ACME_HELLO\nemit: banner\nmessage: edited\n"),
		0o600,
	))

	found, err := extpacks.DiscoverLockedContent(nil)
	testutil.FailErr(t, "DiscoverLockedContent", err)
	pc := findPack(t, found, "acme/shadow")
	if !pc.NeedsReload {
		t.Fatal("linked pack whose files changed must need reload")
	}
	if pc.OmitReason != "" {
		t.Fatalf("dirty development pack must still contribute, omit=%q", pc.OmitReason)
	}
	if len(pc.Units) == 0 {
		t.Fatal("dirty development pack must contribute current units")
	}
}

func TestGitIntegrityFaultDoesNotHideSiblingPack(t *testing.T) {
	t.Setenv("LYCAON_CONFIG_DIR", t.TempDir())
	goodRepo := filepath.Join(t.TempDir(), "good")
	writeReleasePack(t, goodRepo, "acme/good", "1.0.0", nil)
	extpackstest.GitInitCommit(t, goodRepo)
	extpackstest.GitTag(t, goodRepo, "v1.0.0")
	installDevice(t, "file://"+goodRepo, "1.0.0")

	badRepo := filepath.Join(t.TempDir(), "bad")
	writeReleasePack(t, badRepo, "acme/bad", "1.0.0", nil)
	extpackstest.GitInitCommit(t, badRepo)
	extpackstest.GitTag(t, badRepo, "v1.0.0")
	bad := installDevice(t, "file://"+badRepo, "1.0.0")
	testutil.FailErr(t, "tamper cached body", os.WriteFile(
		filepath.Join(bad.PackageRoot, "policy", "ACME_HELLO.yaml"),
		[]byte("id: CHANGED\n"),
		0o600,
	))

	found, err := extpacks.DiscoverLockedContent(nil)
	testutil.FailErr(t, "DiscoverLockedContent", err)
	good := findPack(t, found, "acme/good")
	if good.OmitReason != "" || len(good.Units) == 0 {
		t.Fatalf("sibling pack must still load: omit=%q units=%d", good.OmitReason, len(good.Units))
	}
	isolated := findPack(t, found, "acme/bad")
	if isolated.OmitReason != extpacks.BlockedIntegrity {
		t.Fatalf("tampered pack omit = %q want integrity", isolated.OmitReason)
	}
	if len(isolated.Units) != 0 {
		t.Fatalf("tampered pack must not contribute units: %d", len(isolated.Units))
	}
}
