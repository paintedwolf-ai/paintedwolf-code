package projectroot

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// SessionAddress recognizes an exact host-owned session namespace. The
// returned relative name retains operand bytes and has not been scope-checked.
func SessionAddress(raw string) (area, rel string, ok bool) {
	rest, found := strings.CutPrefix(raw, "@")
	if !found {
		return "", "", false
	}
	label, rel, _ := strings.Cut(rest, "/")
	if !IsVirtualRootLabel(label) || strings.TrimSpace(label) != label {
		return "", "", false
	}
	return strings.ToLower(label), rel, true
}

// SessionPath joins a relative operand under a session directory. It refuses a
// path that is absolute or climbs out of the folder. The check is lexical;
// a caller that opens the path also checks its canonical form.
func SessionPath(dirPath, rel string) (string, error) {
	dir := filepath.Clean(strings.TrimSpace(dirPath))
	if !filepath.IsAbs(dir) {
		return "", fmt.Errorf("session folder %q is not absolute", dirPath)
	}
	if strings.ContainsRune(rel, 0) {
		return "", fmt.Errorf("path contains NUL byte")
	}
	clean := filepath.Clean(filepath.FromSlash(rel))
	if clean == "." {
		return dir, nil
	}
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("%w: %s", ErrPathEscape, rel)
	}
	return filepath.Join(dir, clean), nil
}
