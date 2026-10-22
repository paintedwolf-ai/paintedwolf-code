//go:build darwin || linux

package fseffect

import (
	"os"
	"path/filepath"
	"strings"
)

// Canonical returns an existing descriptor-resolved native identity.
func (r *ReadRoot) Canonical(rel string) (string, error) {
	file, err := r.OpenExact(rel)
	if err != nil {
		return "", err
	}
	defer file.Close()
	return file.Name(), nil
}

// Readlink reads only a link's metadata through its held contained parent.
func (r *ReadRoot) Readlink(rel string, admit func(string) error) (string, error) {
	relative, err := exactReadRelative(rel)
	if err != nil {
		return "", err
	}
	split := strings.LastIndexByte(relative, byte(filepath.Separator))
	parentPath, name := ".", relative
	if split >= 0 {
		parentPath, name = relative[:split], relative[split+1:]
	}
	parent, err := r.OpenExact(parentPath)
	if err != nil {
		return "", err
	}
	defer parent.Close()
	if admit != nil {
		if err := admit(filepath.Join(parent.Name(), name)); err != nil {
			return "", err
		}
	}
	return readLinkAt(int(parent.Fd()), name)
}

// OpenExact uses native OS component order through the same jailed read walk.
func (r *ReadRoot) OpenExact(rel string) (*os.File, error) {
	relative, err := exactReadRelative(rel)
	if err != nil {
		return nil, err
	}
	return openReadContained(r.root, relative)
}
