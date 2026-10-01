//go:build !paintedwolf_release

package bundled_test

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/lycaon/lycaon/internal/scan/bundled"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestCandidateStagesWithoutNetworkAndNeverFallsBack(t *testing.T) {
	f := newCandidateFixture(t)
	t.Setenv(bundled.EnvOpenGrepCandidate, f.directory)
	m, err := bundled.LoadManifest()
	testutil.FailErr(t, "load candidate", err)
	root := t.TempDir()
	path, err := bundled.StageArtifact(t.Context(), m, root, runtime.GOOS, runtime.GOARCH)
	testutil.FailErr(t, "stage candidate without HTTP client", err)
	want := bundled.ArtifactPath(root, m.OpenGrep.Version, runtime.GOOS)
	if path != want {
		t.Fatalf("staged path = %q, want %q", path, want)
	}
	testutil.FailErr(t, "verify staged bytes", bundled.VerifyFileSHA256(path, f.provenance["binary_sha256"].(string)))
	_, err = bundled.StageArtifact(t.Context(), m, root, runtime.GOOS, runtime.GOARCH)
	testutil.FailErr(t, "reuse verified staged bytes", err)
	testutil.FailErr(t, "remove selected candidate", os.Remove(f.binary))
	if _, err := bundled.ResolveOpenGrepBinary(m, root, root); err == nil {
		t.Fatal("missing selected candidate fell back to its staged copy")
	}
	if _, err := bundled.StageArtifact(t.Context(), m, root, runtime.GOOS, runtime.GOARCH); err == nil {
		t.Fatal("missing selected candidate accepted through existing stage")
	}
}

func TestCandidateRevalidatesAllSelectedBytes(t *testing.T) {
	for _, name := range []string{"provenance.json", "source-lock.json", "opengrep"} {
		t.Run(name, func(t *testing.T) {
			f := newCandidateFixture(t)
			t.Setenv(bundled.EnvOpenGrepCandidate, f.directory)
			m, err := bundled.LoadManifest()
			testutil.FailErr(t, "select candidate", err)
			path := filepath.Join(f.directory, name)
			if name == "opengrep" {
				path = f.binary
			}
			testutil.FailErr(t, "mutate selected bytes", os.WriteFile(path, []byte("different selected bytes!!"), 0o755))
			if _, err := bundled.ResolveOpenGrepBinary(m, t.TempDir(), t.TempDir()); err == nil {
				t.Fatal("mutated candidate accepted")
			}
			if _, err := bundled.StageArtifact(t.Context(), m, t.TempDir(), runtime.GOOS, runtime.GOARCH); err == nil {
				t.Fatal("mutated candidate staged")
			}
		})
	}
}

func TestCandidateRejectsRedirectedOrNonExecutableFiles(t *testing.T) {
	for _, name := range []string{"provenance.json", "source-lock.json", "opengrep"} {
		t.Run(name, func(t *testing.T) {
			f := newCandidateFixture(t)
			path := filepath.Join(f.directory, name)
			if name == "opengrep" {
				path = f.binary
			}
			raw, err := os.ReadFile(path)
			testutil.FailErr(t, "read original candidate file", err)
			target := filepath.Join(t.TempDir(), "redirect")
			testutil.FailErr(t, "write redirect target", os.WriteFile(target, raw, 0o755))
			testutil.FailErr(t, "remove original candidate file", os.Remove(path))
			testutil.FailErr(t, "redirect candidate file", os.Symlink(target, path))
			t.Setenv(bundled.EnvOpenGrepCandidate, f.directory)
			if _, err := bundled.LoadManifest(); err == nil {
				t.Fatal("symlinked candidate file accepted")
			}
		})
	}
	if runtime.GOOS != "windows" {
		f := newCandidateFixture(t)
		testutil.FailErr(t, "remove executable permission", os.Chmod(f.binary, 0o600))
		t.Setenv(bundled.EnvOpenGrepCandidate, f.directory)
		if _, err := bundled.LoadManifest(); err == nil {
			t.Fatal("non-executable candidate accepted")
		}
	}
}
