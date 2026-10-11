package confine

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestValidateAttachedWriteRootsAcceptsBroadRootWithoutMutation(t *testing.T) {
	safe := t.TempDir()
	roots := []string{safe, string(filepath.Separator)}
	err := ValidateAttachedWriteRoots(roots)
	if err != nil {
		t.Fatalf("broad attached root refused: %v", err)
	}
	if roots[0] != safe || roots[1] != string(filepath.Separator) {
		t.Fatalf("ValidateAttachedWriteRoots mutated roots: %v", roots)
	}
}

func TestValidateAttachedWriteRootsAcceptsSymlinkToHome(t *testing.T) {
	home, err := os.UserHomeDir()
	testutil.FailErr(t, "resolve home", err)
	link := filepath.Join(t.TempDir(), "root")
	testutil.FailErr(t, "create symlink", os.Symlink(home, link))

	if err := ValidateAttachedWriteRoots([]string{link}); err != nil {
		t.Fatalf("broad attached root refused: %v", err)
	}
}

func TestValidateAttachedWriteRootsAllowsSafeRoot(t *testing.T) {
	root := t.TempDir()
	if err := ValidateAttachedWriteRoots([]string{root}); err != nil {
		t.Fatalf("validate safe root %q: %v; read-deny=%v write-deny=%v", root, err, SecretReadDenyRoots(), KeyMaterialWritePaths())
	}
}

// Broad ancestors retain inner floors; a store alias stays refused.
func TestValidateGrantedWriteRootsLane(t *testing.T) {
	SetCredentialStorePathsSource(func() []string { return credentialStorePathFixture })
	t.Cleanup(func() { SetCredentialStorePathsSource(nil) })
	home, err := os.UserHomeDir()
	testutil.FailErr(t, "resolve home", err)

	dockerParent := filepath.Join(home, ".docker")
	if err := ValidateGrantedWriteRoots([]string{dockerParent}); err != nil {
		t.Fatalf("granted store ancestor refused: %v", err)
	}
	if err := ValidateAttachedWriteRoots([]string{dockerParent}); err != nil {
		t.Fatalf("attached store ancestor refused: %v", err)
	}
	if err := ValidateGrantedWriteRoots([]string{home}); err != nil {
		t.Fatalf("granted home refused: %v", err)
	}

	link := filepath.Join(t.TempDir(), "store")
	testutil.FailErr(t, "create symlink", os.Symlink(filepath.Join(home, ".kube"), link))
	if err := ValidateGrantedWriteRoots([]string{link}); !errors.Is(err, ErrWriteRootRefused) {
		t.Fatalf("symlink alias of a store accepted on the granted lane: %v", err)
	}
}

func TestValidateEffectiveWriteRootsAcceptsExplicitBroadRoots(t *testing.T) {
	home, err := os.UserHomeDir()
	testutil.FailErr(t, "resolve home", err)
	for _, root := range []string{home, string(filepath.Separator)} {
		if err := validateEffectiveWriteRoots([]string{root}); err != nil {
			t.Fatalf("broad root %s refused: %v", root, err)
		}
	}
	if err := validateEffectiveWriteRoots([]string{"relative"}); err == nil {
		t.Fatal("relative root accepted")
	}
}
