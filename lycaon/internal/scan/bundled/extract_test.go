package bundled_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/scan/bundled"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestResolveOpenGrepBinaryFromEngineBundledDir(t *testing.T) {
	f := newBuildFixture(t)
	root := t.TempDir()
	path, err := bundled.StageArtifact(t.Context(), f.selectArtifact(t), root, f.goos, f.goarch)
	testutil.FailErr(t, "stage", err)
	got, err := bundled.ResolveOpenGrepBinary(f.runtimeManifestForHost(t), t.TempDir(), root)
	testutil.FailErr(t, "resolve staged artifact", err)
	if got != path {
		t.Fatalf("resolved %s, want %s", got, path)
	}
}

func TestResolveRejectsPathFallbackAndStaleBundle(t *testing.T) {
	f := newBuildFixture(t)
	m := f.runtimeManifestForHost(t)
	t.Setenv("PATH", f.directory)
	root := t.TempDir()
	stale := bundled.ArtifactPath(root, "0.0.1", f.goos)
	testutil.FailErr(t, "create stale directory", os.MkdirAll(filepath.Dir(stale), 0o755))
	writeBuildFile(t, filepath.Dir(stale), filepath.Base(stale), []byte("stale"))
	_, err := bundled.ResolveOpenGrepBinary(m, t.TempDir(), root)
	if !errors.Is(err, bundled.ErrOpenGrepNotFound) {
		t.Fatalf("unexpected fallback result: %v", err)
	}
}
