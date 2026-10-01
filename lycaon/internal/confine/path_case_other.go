//go:build !darwin

package confine

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// volumeFoldsCase asks each ancestor in turn until one can answer.
func volumeFoldsCase(dir string) bool {
	for p := dir; ; {
		if folds, decided := probeFold(p); decided {
			return folds
		}
		parent := filepath.Dir(p)
		if parent == p {
			return true
		}
		p = parent
	}
}

// probeFold compares an existing name with its case-flipped spelling.
func probeFold(dir string) (folds, decided bool) {
	handle, err := os.Open(dir) // #nosec G304 -- dir is an existing ancestor of a caller-supplied path.
	if err != nil {
		return false, false
	}
	defer func() { _ = handle.Close() }()
	names, err := handle.Readdirnames(probeEntryLimit)
	if err != nil && len(names) == 0 {
		return false, false
	}
	for _, name := range names {
		flipped := flipASCIICase(name)
		if flipped == name {
			continue
		}
		original, err := os.Lstat(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		alternate, err := os.Lstat(filepath.Join(dir, flipped))
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return false, true
			}
			continue
		}
		// Distinct spellings may coexist on case-sensitive volumes.
		return os.SameFile(original, alternate), true
	}
	return false, false
}

// probeEntryLimit bounds filesystem probing.
const probeEntryLimit = 64

// flipASCIICase preserves byte length while changing one letter.
func flipASCIICase(name string) string {
	for i := 0; i < len(name); i++ {
		switch c := name[i]; {
		case c >= 'a' && c <= 'z':
			return name[:i] + string(rune(c-'a'+'A')) + name[i+1:]
		case c >= 'A' && c <= 'Z':
			return name[:i] + string(rune(c-'A'+'a')) + name[i+1:]
		}
	}
	return name
}
