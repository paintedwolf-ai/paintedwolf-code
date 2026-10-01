package confine

import (
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/fspath"
)

// WriteSubjectKind classifies what a refused write was about.
type WriteSubjectKind string

const (
	// WriteSubjectOrdinary permits a coalesced directory proposal.
	WriteSubjectOrdinary WriteSubjectKind = "ordinary"
	// WriteSubjectKeyMaterial permits exact chat grants.
	WriteSubjectKeyMaterial WriteSubjectKind = "key_material"
	// WriteSubjectCredentialStore permits one file transaction.
	WriteSubjectCredentialStore WriteSubjectKind = "credential_store"
)

// WriteSubject describes a denied write and its available authority.
type WriteSubject struct {
	Kind WriteSubjectKind
	// GrantPath is the exact credential file covered.
	GrantPath string
}

// ClassifyBlockedWrite resolves a denied path against credential policy.
func ClassifyBlockedWrite(blockedPath string) WriteSubject {
	path := filepath.Clean(strings.TrimSpace(blockedPath))
	if path == "" || path == "." || !filepath.IsAbs(path) {
		return WriteSubject{Kind: WriteSubjectOrdinary}
	}
	// Canonical on both sides: the catalogs resolve the same way.
	path = fspath.CanonicalPath(path)
	// Classification checks descendants, not capability-bearing ancestors.
	if underKeyMaterial(path) {
		return WriteSubject{Kind: WriteSubjectKeyMaterial}
	}
	target, ok := resolveCredentialTarget(path)
	if !ok {
		return WriteSubject{Kind: WriteSubjectOrdinary}
	}
	return WriteSubject{Kind: WriteSubjectCredentialStore, GrantPath: target}
}

// resolveCredentialTarget maps rewrite siblings to their credential file.
func resolveCredentialTarget(path string) (string, bool) {
	path = filepath.Clean(path)
	dir, base := filepath.Split(path)
	dir = filepath.Clean(dir)

	for _, store := range CredentialStorePaths() {
		store = filepath.Clean(store)
		if store == "" || store == "." {
			continue
		}
		// A rewrite sibling of a catalogued file, in that file's directory.
		if PathEqual(filepath.Dir(store), dir) && NameContains(store, base, filepath.Base(store)) {
			return store, true
		}
	}
	// Otherwise the path itself, when it is a catalogued store or sits inside one.
	if underCredentialStore(path) {
		return path, true
	}
	return "", false
}

// underCredentialStore reports whether path is a catalogued store or sits inside one.
func underCredentialStore(path string) bool { return underAny(path, CredentialStorePaths()) }

// ProtectedPathsWithin returns protected paths at or below root.
func ProtectedPathsWithin(root string) []string {
	p := filepath.Clean(strings.TrimSpace(root))
	if p == "" || p == "." || !filepath.IsAbs(p) {
		return nil
	}
	p = fspath.CanonicalPath(p)
	var out []string
	for _, protected := range append(CredentialStorePaths(), KeyMaterialWritePaths()...) {
		protected = filepath.Clean(protected)
		if protected == "" || protected == "." {
			continue
		}
		if PathAtOrUnder(protected, p) {
			out = append(out, protected)
		}
	}
	return out
}

// KeyMaterialPath reports whether path sits at or under the key-material floor.
func KeyMaterialPath(path string) bool {
	p := filepath.Clean(strings.TrimSpace(path))
	if p == "" || p == "." || !filepath.IsAbs(p) {
		return false
	}
	return underKeyMaterial(fspath.CanonicalPath(p))
}

// underKeyMaterial reports whether path is the floor or sits inside it.
func underKeyMaterial(path string) bool { return underAny(path, KeyMaterialWritePaths()) }

// underAny checks whether path equals or descends from a root. Comparison folds
// case where the filesystem does — see path_case.go.
func underAny(path string, roots []string) bool {
	for _, root := range roots {
		if strings.TrimSpace(root) == "" || filepath.Clean(root) == "." {
			continue
		}
		if PathAtOrUnder(path, root) {
			return true
		}
	}
	return false
}
