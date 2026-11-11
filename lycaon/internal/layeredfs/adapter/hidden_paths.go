package adapter

import (
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/fspath"
)

// HiddenRelsUnder names, relative to root, the engine storage in hidden that root contains.
func HiddenRelsUnder(root string, hidden []string) []string {
	canonicalRoot := fspath.CanonicalPath(root)
	var rels []string
	for _, path := range hidden {
		rel, err := filepath.Rel(canonicalRoot, fspath.CanonicalPath(path))
		if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		rels = append(rels, filepath.ToSlash(rel))
	}
	return rels
}
