package confine

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

// requireFoldingFilesystem skips when the volume under dir is case-sensitive,
// where ~/.ssh and ~/.SSH are different directories.
func requireFoldingFilesystem(t *testing.T, dir string) {
	t.Helper()
	if !FoldsCase(dir) {
		t.Skip("case-sensitive filesystem: a case-variant spelling is a different file here")
	}
}

func TestFoldsCaseMatchesTheFilesystem(t *testing.T) {
	dir := t.TempDir()
	name := filepath.Join(dir, "Probe")
	testutil.FailErr(t, "seed probe file", os.WriteFile(name, []byte("x"), 0o600))

	_, statErr := os.Stat(filepath.Join(dir, "probe"))
	// The probe must agree with what the filesystem actually answered.
	if got, want := FoldsCase(dir), statErr == nil; got != want {
		t.Fatalf("FoldsCase = %v, but stat of the flipped spelling succeeded = %v", got, want)
	}
}

func TestPathAtOrUnderFoldsCaseWhereTheFilesystemDoes(t *testing.T) {
	dir := t.TempDir()
	testutil.FailErr(t, "mkdir .ssh", os.MkdirAll(filepath.Join(dir, ".ssh"), 0o700))
	requireFoldingFilesystem(t, dir)

	root := filepath.Join(dir, ".ssh")
	variant := filepath.Join(dir, ".SSH", "id_rsa")
	if !PathAtOrUnder(variant, root) {
		t.Fatalf("PathAtOrUnder(%q, %q) = false, want true", variant, root)
	}
	if !PathEqual(filepath.Join(dir, ".SSH"), root) {
		t.Fatal("PathEqual must fold a case-variant spelling of the same directory")
	}
}

func TestPathAtOrUnderRejectsUnrelatedPaths(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, ".ssh")
	for _, p := range []string{
		filepath.Join(dir, ".sshkeys", "id_rsa"),
		filepath.Join(dir, "ssh"),
		filepath.Join(dir, "other"),
	} {
		if PathAtOrUnder(p, root) {
			t.Errorf("PathAtOrUnder(%q, %q) = true, want false", p, root)
		}
	}
}

// Key-material restrictions apply to case variants of the same path.
func TestKeyMaterialPathFoldsCase(t *testing.T) {
	home := t.TempDir()
	testutil.FailErr(t, "mkdir .ssh", os.MkdirAll(filepath.Join(home, ".ssh"), 0o700))
	requireFoldingFilesystem(t, home)
	restore := installKeyMaterialFloor(t, filepath.Join(home, ".ssh"))
	defer restore()

	if !KeyMaterialPath(filepath.Join(home, ".ssh", "id_rsa")) {
		t.Fatal("byte-exact key-material path must match")
	}
	if !KeyMaterialPath(filepath.Join(home, ".SSH", "id_rsa")) {
		t.Fatal("case-variant key-material path must match on a folding filesystem")
	}
}

// ClassifyBlockedWrite drives the approval ladder: an unrecognized path is
// offered as an ordinary directory grant.
func TestClassifyBlockedWriteFoldsCase(t *testing.T) {
	home := t.TempDir()
	testutil.FailErr(t, "mkdir .ssh", os.MkdirAll(filepath.Join(home, ".ssh"), 0o700))
	requireFoldingFilesystem(t, home)
	restore := installKeyMaterialFloor(t, filepath.Join(home, ".ssh"))
	defer restore()

	subject := ClassifyBlockedWrite(filepath.Join(home, ".SSH", "id_rsa"))
	if subject.Kind != WriteSubjectKeyMaterial {
		t.Fatalf("kind = %q, want %q — a case-variant spelling must not read as ordinary",
			subject.Kind, WriteSubjectKeyMaterial)
	}
}

// Control-plane restrictions apply to case variants of the config directory.
func TestControlPlaneWriteDeniedFoldsCase(t *testing.T) {
	root := t.TempDir()
	configDir := filepath.Join(root, "PaintedWolf")
	testutil.FailErr(t, "mkdir config dir", os.MkdirAll(configDir, 0o700))
	requireFoldingFilesystem(t, root)
	t.Setenv("LYCAON_CONFIG_DIR", configDir)

	exact := filepath.Join(configDir, "api.token")
	if !ControlPlaneWriteDenied(exact) {
		t.Fatalf("ControlPlaneWriteDenied(%q) = false, want true", exact)
	}
	variant := filepath.Join(root, "paintedwolf", "api.token")
	if !ControlPlaneWriteDenied(variant) {
		t.Fatalf("ControlPlaneWriteDenied(%q) = false — a case-variant spelling reached the control plane", variant)
	}
}

// installKeyMaterialFloor points the pack-derived floor at an absolute test
// directory and restores whatever was installed before.
func installKeyMaterialFloor(t *testing.T, roots ...string) func() {
	t.Helper()
	previous := keyMaterialPathsSource.Load()
	SetKeyMaterialPathsSource(func() []string { return roots })
	return func() {
		if previous == nil {
			SetKeyMaterialPathsSource(nil)
			return
		}
		SetKeyMaterialPathsSource(*previous)
	}
}
