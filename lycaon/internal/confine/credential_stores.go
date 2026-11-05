package confine

import (
	"github.com/lycaon/lycaon/internal/fspath"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
)

// keyMaterialPathsSource supplies the catalogued key-material floor, installed
// fail-closed at boot from the shipped pack. It and the CLI credential-file
// stores are read together at every refusal, so both are pack data.
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
// denied for reads until a declared read_path or credential_store grant opens
// an exception. CLI credential files outside this floor stay readable.
func KeyMaterialReadDenyPaths() []string { return resolveEach(keyMaterialTreeRoots()) }

var osCredentialStoreRelDirs = []string{
	filepath.Join("Library", "Keychains"),
	filepath.Join(".local", "share", "keyrings"),
	filepath.Join(".local", "share", "kwalletd"),
	filepath.Join("AppData", "Roaming", "Microsoft", "Credentials"),
	filepath.Join("AppData", "Local", "Microsoft", "Credentials"),
	filepath.Join("AppData", "Local", "Microsoft", "Vault"),
}

// OSCredentialStoreReadDenyPaths returns the resolved OS credential store paths
// within the catalogued key-material read floor.
func OSCredentialStoreReadDenyPaths() []string {
	var raw []string
	for _, p := range catalogedKeyMaterialPaths() {
		rel := strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(p), "~/"), "/")
		if rel == "" || strings.HasPrefix(rel, "/") {
			continue
		}
		cleanRel := filepath.Clean(rel)
		for _, storeRel := range osCredentialStoreRelDirs {
			if cleanRel == storeRel {
				raw = append(raw, p)
				break
			}
		}
	}
	return resolveEach(expandHomePaths(raw))
}

func isOSCredentialStoreDenyPath(p string) bool {
	for _, storePath := range OSCredentialStoreReadDenyPaths() {
		if PathEqual(p, storePath) {
			return true
		}
	}
	return false
}

// credentialStorePathsSource supplies catalogued CLI credential-file paths for
// the write floor (distinct from Request.CredentialStore, which governs read
// access to the host OS credential store).
var credentialStorePathsSource atomic.Pointer[func() []string]

// SetCredentialStorePathsSource installs the pack-derived CLI credential-file write floor paths.
func SetCredentialStorePathsSource(fn func() []string) {
	if fn == nil {
		credentialStorePathsSource.Store(nil)
		return
	}
	credentialStorePathsSource.Store(&fn)
}

// CredentialStorePaths returns resolved catalogued CLI credential-file write floor paths.
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

var darwinLoginKeychainFiles = []string{
	"login.keychain-db",
	"login.keychain",
}

var linuxCredentialStoreRelDirs = []string{
	filepath.Join(".local", "share", "keyrings"),
	filepath.Join(".local", "share", "kwalletd"),
}

// darwinLoginKeychainLiterals returns the exact login keychain database paths
// admitted read-only when CredentialStore is granted.
func darwinLoginKeychainLiterals() []string {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return nil
	}
	lexicalDir := filepath.Clean(filepath.Join(home, "Library", "Keychains"))
	canonicalDir := fspath.CanonicalPath(lexicalDir)
	if canonicalDir == "" {
		canonicalDir = lexicalDir
	}
	seen := map[string]bool{}
	var out []string
	add := func(p string) {
		p = strings.TrimSpace(p)
		if p == "" || seen[p] {
			return
		}
		seen[p] = true
		out = append(out, p)
	}
	for _, name := range darwinLoginKeychainFiles {
		add(filepath.Join(canonicalDir, name))
		if resolved := fspath.CanonicalPath(filepath.Join(canonicalDir, name)); resolved != "" && PathAtOrUnder(resolved, canonicalDir) {
			add(resolved)
		}
		if lexicalDir != canonicalDir {
			add(filepath.Join(lexicalDir, name))
		}
	}
	sort.Strings(out)
	return out
}

// darwinLoginKeychainMetadataDirs returns the Keychains directory paths that
// require file-read-metadata traversal when CredentialStore is granted.
func darwinLoginKeychainMetadataDirs() []string {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return nil
	}
	lexicalDir := filepath.Clean(filepath.Join(home, "Library", "Keychains"))
	canonicalDir := fspath.CanonicalPath(lexicalDir)
	if canonicalDir == "" || canonicalDir == lexicalDir {
		return []string{lexicalDir}
	}
	out := []string{canonicalDir, lexicalDir}
	sort.Strings(out)
	return out
}

// linuxCredentialStoreReadRoots returns the Secret Service and KWallet store
// directories admitted read-only when CredentialStore is granted.
func linuxCredentialStoreReadRoots() []string {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return nil
	}
	shareLexical := filepath.Clean(filepath.Join(home, ".local", "share"))
	shareCanonical := fspath.CanonicalPath(shareLexical)
	if shareCanonical == "" {
		shareCanonical = shareLexical
	}
	controlRoots := resolveEach(controlPlaneReadDenyRoots())
	seen := map[string]bool{}
	var out []string
	add := func(p string) {
		p = strings.TrimSpace(p)
		if p == "" || seen[p] {
			return
		}
		for _, root := range controlRoots {
			if PathAtOrUnder(p, root) {
				return
			}
		}
		seen[p] = true
		out = append(out, p)
	}
	for _, rel := range linuxCredentialStoreRelDirs {
		base := filepath.Base(rel)
		expectedCanonical := filepath.Join(shareCanonical, base)
		lexical := filepath.Clean(filepath.Join(home, rel))
		resolved := fspath.CanonicalPath(lexical)
		if resolved == "" {
			resolved = expectedCanonical
		}
		// Reject symlinks that redirect the store outside its expected location.
		if !PathAtOrUnder(resolved, expectedCanonical) {
			continue
		}
		add(resolved)
		if lexical != resolved {
			add(lexical)
		}
	}
	sort.Strings(out)
	return out
}

func mergeSortedStrings(a, b []string) []string {
	if len(b) == 0 {
		return a
	}
	seen := make(map[string]bool, len(a)+len(b))
	out := make([]string, 0, len(a)+len(b))
	for _, list := range [][]string{a, b} {
		for _, item := range list {
			item = strings.TrimSpace(item)
			if item == "" || seen[item] {
				continue
			}
			seen[item] = true
			out = append(out, item)
		}
	}
	sort.Strings(out)
	return out
}
