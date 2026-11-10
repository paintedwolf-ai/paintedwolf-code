package checkpoint

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

// Exercise publication itself independently of worker claims, Git graph walks,
// and outage control. The parent is an explicitly provisioned network mount.
func TestNetworkGitAssetPublicationPreservesBytes(t *testing.T) {
	parent := os.Getenv("PW_M14_GIT_PUBLICATION_PARENT")
	if parent == "" {
		t.Skip("requires isolated network publication fixture")
	}
	target, err := os.MkdirTemp(parent, "m14-git-publication-")
	testutil.FailErr(t, "create network target", err)
	t.Cleanup(func() { testutil.FailErr(t, "remove authored network target", os.RemoveAll(target)) })
	payload := bytes.Repeat([]byte("publication fixture\n"), 64)
	source := filepath.Join(t.TempDir(), "asset.idx")
	testutil.FailErr(t, "local publication source", os.WriteFile(source, payload, 0o444))
	destination := filepath.Join(target, "asset.idx")
	for attempt := range 2 {
		t.Logf("publication attempt=%d starting", attempt)
		testutil.FailErr(t, "publish asset", copyFileStreamingAtomic(source, destination, 0o444))
		t.Logf("publication attempt=%d returned", attempt)
		got, err := os.ReadFile(destination)
		testutil.FailErr(t, "read published asset", err)
		if !bytes.Equal(got, payload) {
			t.Fatal("publication changed bytes")
		}
	}
	entries, err := os.ReadDir(target)
	testutil.FailErr(t, "inspect publication residue", err)
	if len(entries) != 1 || entries[0].Name() != "asset.idx" {
		t.Fatalf("successful publication retained staging files: %v", entries)
	}
}
