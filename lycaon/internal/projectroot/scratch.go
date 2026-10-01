package projectroot

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ScratchAddress reports whether raw addresses the invoking session's scratch
// folder, as `@scratch` or `@scratch/<path>`, and returns the slash path below
// that folder ("" for the folder itself). The label compares case-insensitively.
// The returned path is not checked for escape; ScratchPath does that.
func ScratchAddress(raw string) (rel string, ok bool) {
	rest, found := strings.CutPrefix(strings.TrimSpace(raw), "@")
	if !found {
		return "", false
	}
	label, rel, _ := strings.Cut(rest, "/")
	if !strings.EqualFold(label, VirtualScratchLabel) {
		return "", false
	}
	return rel, true
}

// ScratchPath joins a scratch-relative path under scratchDir. It refuses a
// path that is absolute or climbs out of the folder. The check is lexical;
// a caller that opens the path also checks its canonical form.
func ScratchPath(scratchDir, rel string) (string, error) {
	dir := filepath.Clean(strings.TrimSpace(scratchDir))
	if !filepath.IsAbs(dir) {
		return "", fmt.Errorf("session scratch folder %q is not absolute", scratchDir)
	}
	if strings.ContainsRune(rel, 0) {
		return "", fmt.Errorf("path contains NUL byte")
	}
	clean := filepath.Clean(filepath.FromSlash(rel))
	if clean == "." {
		return dir, nil
	}
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("%w: @%s/%s", ErrPathEscape, VirtualScratchLabel, rel)
	}
	return filepath.Join(dir, clean), nil
}
