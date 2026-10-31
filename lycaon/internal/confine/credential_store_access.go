package confine

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/fspath"
)

var darwinLoginKeychainFiles = []string{
	"login.keychain-db",
	"login.keychain",
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
