package confine

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestValidateAttachedWriteRootsRefusesStateAncestorWithoutMutation(t *testing.T) {
	safe := t.TempDir()
	t.Setenv("LYCAON_CONFIG_DIR", filepath.Join(safe, "state"))
	roots := []string{safe, string(filepath.Separator)}
	err := ValidateAttachedWriteRoots(roots)
	if err == nil || err.Code != WriteRootCodeContainsState {
		t.Fatalf("state ancestor root was admitted: %v", err)
	}
	if roots[0] != safe || roots[1] != string(filepath.Separator) {
		t.Fatalf("ValidateAttachedWriteRoots mutated roots: %v", roots)
	}
}

func TestValidateAttachedWriteRootsRefusesAliasOfStateAncestor(t *testing.T) {
	home, err := os.UserHomeDir()
	testutil.FailErr(t, "resolve home", err)
	t.Setenv("LYCAON_CONFIG_DIR", filepath.Join(home, ".config", "fixture-state"))
	link := filepath.Join(t.TempDir(), "root")
	testutil.FailErr(t, "create symlink", os.Symlink(home, link))

	if err := ValidateAttachedWriteRoots([]string{link}); err == nil || err.Code != WriteRootCodeContainsState {
		t.Fatalf("state ancestor alias admitted: %v", err)
	}
}

func TestValidateAttachedWriteRootsAllowsSafeRoot(t *testing.T) {
	root := t.TempDir()
	if err := ValidateAttachedWriteRoots([]string{root}); err != nil {
		t.Fatalf("validate safe root %q: %v; read-deny=%v write-deny=%v", root, err, SecretReadDenyRoots(), KeyMaterialWritePaths())
	}
}

// Store ancestors retain their exact-path floors; state ancestors cannot be roots.
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
	t.Setenv("LYCAON_CONFIG_DIR", filepath.Join(home, ".config", "fixture-state"))
	if err := ValidateGrantedWriteRoots([]string{home}); err == nil || err.Code != WriteRootCodeContainsState {
		t.Fatalf("state ancestor grant admitted: %v", err)
	}

	link := filepath.Join(t.TempDir(), "store")
	testutil.FailErr(t, "create symlink", os.Symlink(filepath.Join(home, ".kube"), link))
	if err := ValidateGrantedWriteRoots([]string{link}); !errors.Is(err, ErrWriteRootRefused) {
		t.Fatalf("symlink alias of a store accepted on the granted lane: %v", err)
	}
}

func TestValidateEffectiveWriteRootsRefusesStateAncestors(t *testing.T) {
	home, err := os.UserHomeDir()
	testutil.FailErr(t, "resolve home", err)
	t.Setenv("LYCAON_CONFIG_DIR", filepath.Join(home, ".config", "fixture-state"))
	for _, root := range []string{home, string(filepath.Separator)} {
		if err := validateEffectiveWriteRoots([]string{root}); err == nil || err.Code != WriteRootCodeContainsState {
			t.Fatalf("state ancestor %s admitted: %v", root, err)
		}
	}
	if err := validateEffectiveWriteRoots([]string{"relative"}); err == nil {
		t.Fatal("relative root accepted")
	}
}
