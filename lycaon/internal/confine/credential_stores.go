package confine

import (
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
)

// keyMaterialPathsSource supplies the catalogued key-material floor, installed
// fail-closed at boot from the shipped pack. It and the credential stores are
// read together at every refusal, so both are pack data.
var keyMaterialPathsSource atomic.Pointer[func() []string]

// SetKeyMaterialPathsSource installs the pack-derived key-material floor.
func SetKeyMaterialPathsSource(fn func() []string) {
	if fn == nil {
		keyMaterialPathsSource.Store(nil)
		return
	}
	keyMaterialPathsSource.Store(&fn)
}

// KeyMaterialHomeRelPaths returns the floor with the catalogue's `~/` prefix and
// trailing separator removed, ready to join onto a home directory.
func KeyMaterialHomeRelPaths() []string {
	catalogued := catalogedKeyMaterialPaths()
	out := make([]string, 0, len(catalogued))
	for _, p := range catalogued {
		rel := strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(p), "~/"), "/")
		if rel == "" || strings.HasPrefix(rel, "/") {
			continue
		}
		out = append(out, filepath.Clean(rel))
	}
	return out
}

// catalogedKeyMaterialPaths returns the raw catalogue entries, or nil when no
// source is installed.
func catalogedKeyMaterialPaths() []string {
	fn := keyMaterialPathsSource.Load()
	if fn == nil {
		return nil
	}
	return (*fn)()
}

// keyMaterialTreeRoots resolves the floor under the user's home directory.
func keyMaterialTreeRoots() []string {
	return expandHomePaths(catalogedKeyMaterialPaths())
}

// KeyMaterialWritePaths returns resolved protected key paths.
func KeyMaterialWritePaths() []string { return resolveEach(keyMaterialTreeRoots()) }

// KeyMaterialReadDenyPaths is the command-plane read floor: the same key trees,
// denied for reads until a declared read_path grant punches an exact exception.
// Credential stores stay readable — CLIs authenticate from them.
func KeyMaterialReadDenyPaths() []string { return resolveEach(keyMaterialTreeRoots()) }

// credentialStorePathsSource supplies catalogued credential paths.
var credentialStorePathsSource atomic.Pointer[func() []string]

// SetCredentialStorePathsSource installs the pack-derived credential store paths.
func SetCredentialStorePathsSource(fn func() []string) {
	if fn == nil {
		credentialStorePathsSource.Store(nil)
		return
	}
	credentialStorePathsSource.Store(&fn)
}

// CredentialStorePaths returns resolved catalogued stores.
func CredentialStorePaths() []string {
	fn := credentialStorePathsSource.Load()
	if fn == nil {
		return nil
	}
	return resolveEach(expandHomePaths((*fn)()))
}

func expandHomePaths(paths []string) []string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = ""
	}
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		p = strings.TrimSpace(p)
		switch {
		case p == "":
			continue
		case strings.HasPrefix(p, "~/"):
			if home == "" {
				continue
			}
			out = append(out, filepath.Join(home, p[2:]))
		case filepath.IsAbs(p):
			out = append(out, filepath.Clean(p))
		}
	}
	return out
}
