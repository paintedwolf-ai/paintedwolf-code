package confine

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestValidateAttachedWriteRootsRefusesUnsafeRootWithoutMutation(t *testing.T) {
	safe := t.TempDir()
	roots := []string{safe, string(filepath.Separator)}
	err := ValidateAttachedWriteRoots(roots)
	if !errors.Is(err, ErrWriteRootRefused) {
		t.Fatalf("ValidateAttachedWriteRoots error = %v, want ErrWriteRootRefused", err)
	}
	if roots[0] != safe || roots[1] != string(filepath.Separator) {
		t.Fatalf("ValidateAttachedWriteRoots mutated roots: %v", roots)
	}
}

func TestValidateAttachedWriteRootsRefusesSymlinkToHome(t *testing.T) {
	home, err := os.UserHomeDir()
	testutil.FailErr(t, "resolve home", err)
	link := filepath.Join(t.TempDir(), "root")
	testutil.FailErr(t, "create symlink", os.Symlink(home, link))

	err = ValidateAttachedWriteRoots([]string{link})
	if !errors.Is(err, ErrWriteRootRefused) {
		t.Fatalf("ValidateAttachedWriteRoots error = %v, want ErrWriteRootRefused", err)
	}
}

func TestValidateAttachedWriteRootsAllowsSafeRoot(t *testing.T) {
	root := t.TempDir()
	if err := ValidateAttachedWriteRoots([]string{root}); err != nil {
		t.Fatalf("validate safe root %q: %v; read-deny=%v write-deny=%v", root, err, SecretReadDenyRoots(), KeyMaterialWritePaths())
	}
}

// The granted lane accepts a reviewed store-ancestor lease that the attached
// lane refuses, and a symlink alias of a store fails the same as the store.
func TestValidateGrantedWriteRootsLane(t *testing.T) {
	SetCredentialStorePathsSource(func() []string { return credentialStorePathFixture })
	t.Cleanup(func() { SetCredentialStorePathsSource(nil) })
	home, err := os.UserHomeDir()
	testutil.FailErr(t, "resolve home", err)

	dockerParent := filepath.Join(home, ".docker")
	if err := ValidateGrantedWriteRoots([]string{dockerParent}); err != nil {
		t.Fatalf("granted store ancestor refused: %v", err)
	}
	if err := ValidateAttachedWriteRoots([]string{dockerParent}); !errors.Is(err, ErrWriteRootRefused) {
		t.Fatalf("attached store ancestor accepted; the lanes must differ exactly here")
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

func TestValidateEffectiveWriteRootsRejectsAmbientBroadRoots(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("home: %v", err)
	}
	for _, root := range []string{string(filepath.Separator), home, "relative"} {
		if err := validateEffectiveWriteRoots([]string{root}, nil); err == nil {
			t.Fatalf("effective root %q accepted", root)
		}
	}
}

func TestValidateEffectiveWriteRootsAcceptsGrantedHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("home: %v", err)
	}
	granted := map[string]bool{strings.TrimRight(fspath.CanonicalPath(home), "/"): true}
	if err := validateEffectiveWriteRoots([]string{home}, granted); err != nil {
		t.Fatalf("granted home refused: %v", err)
	}
	if err := validateEffectiveWriteRoots([]string{string(filepath.Separator)}, granted); err == nil {
		t.Fatal("the filesystem root was accepted; no grant covers that")
	}
}
