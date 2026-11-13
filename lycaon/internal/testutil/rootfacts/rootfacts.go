// Package rootfacts installs a fixture home with one folder of each root
// admission class, so tests exercise the real standing write-root check.
package rootfacts

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/confine"
)

// Facts names the fixture folders. Every folder exists.
type Facts struct {
	Home string
	// Work is an ordinary folder agents may use.
	Work string
	// State is the engine's state tree.
	State string
	// ContainsState is the folder holding the state tree.
	ContainsState string
	// InsideState lies in the state tree.
	InsideState string
	// KeyMaterial is a catalogued key-material folder.
	KeyMaterial string
	// CredentialStore is a catalogued CLI credential folder.
	CredentialStore string
}

// Install points HOME at a fixture and installs one key-material and one
// credential-store floor path. It keeps a state tree a harness already chose
// and otherwise places one in the fixture home. The previous floor sources are
// restored on cleanup, so callers must not run in parallel.
func Install(t testing.TB) Facts {
	t.Helper()
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve fixture home: %v", err)
	}
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	if os.Getenv("LYCAON_CONFIG_DIR") == "" {
		t.Setenv("LYCAON_CONFIG_DIR", filepath.Join(home, ".config", "paintedwolf"))
	}
	state, err := configdir.Root()
	if err != nil {
		t.Fatalf("resolve fixture state tree: %v", err)
	}
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatalf("create fixture state tree: %v", err)
	}
	if state, err = filepath.EvalSymlinks(state); err != nil {
		t.Fatalf("resolve fixture state tree: %v", err)
	}
	facts := Facts{
		Home:            home,
		Work:            filepath.Join(home, "work", "repo"),
		State:           state,
		ContainsState:   filepath.Dir(state),
		InsideState:     filepath.Join(state, "inside"),
		KeyMaterial:     filepath.Join(home, ".ssh"),
		CredentialStore: filepath.Join(home, ".aws"),
	}
	for _, dir := range []string{facts.Work, facts.InsideState, facts.KeyMaterial, facts.CredentialStore} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("create fixture folder: %v", err)
		}
	}
	previousKeys := confine.SetKeyMaterialPathsSource(func() []string { return []string{"~/.ssh/"} })
	previousStores := confine.SetCredentialStorePathsSource(func() []string { return []string{"~/.aws/"} })
	t.Cleanup(func() {
		confine.SetKeyMaterialPathsSource(previousKeys)
		confine.SetCredentialStorePathsSource(previousStores)
	})
	return facts
}

// EditorOnly lists one folder of each editor-only class with the code the
// standing write-root check returns for it.
func (f Facts) EditorOnly() []Root {
	return []Root{
		{Name: "contains state", Path: f.ContainsState, Code: confine.WriteRootCodeContainsState},
		{Name: "inside state", Path: f.InsideState, Code: confine.WriteRootCodeSecretStore},
		{Name: "key material", Path: f.KeyMaterial, Code: confine.WriteRootCodeSecretStore},
		{Name: "credential store", Path: f.CredentialStore, Code: confine.WriteRootCodeSecretStore},
	}
}

// Root is one classified fixture folder.
type Root struct {
	Name string
	Path string
	Code string
}
