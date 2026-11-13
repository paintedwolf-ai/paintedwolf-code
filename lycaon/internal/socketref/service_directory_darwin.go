//go:build darwin

package socketref

import (
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// ServiceDirectory makes a private directory for a socket the host serves to
// confined actions. It lies in the short-address namespace that every
// confinement's write floor denies, so no action can repoint its path.
func ServiceDirectory() (string, error) {
	dir, err := os.MkdirTemp("/private/tmp", shortReferencePrefix())
	if err != nil {
		return "", err
	}
	var st unix.Stat_t
	if err := unix.Lstat(dir, &st); err != nil || st.Mode&unix.S_IFMT != unix.S_IFDIR || st.Mode&0777 != 0700 || st.Uid != uint32(os.Getuid()) {
		_ = os.Remove(dir)
		return "", ErrUnbound
	}
	return dir, nil
}

// IsServicePath reports whether path names an entry directly inside a
// ServiceDirectory.
func IsServicePath(path string) bool {
	dir := filepath.Dir(filepath.Clean(path))
	return filepath.Dir(dir) == "/private/tmp" && strings.HasPrefix(filepath.Base(dir), shortReferencePrefix())
}
