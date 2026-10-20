//go:build windows

package fseffect

import (
	"os"
	"strings"
)

// Canonical returns the native path admitted by the existing handle-relative
// reader. Missing-leaf and link metadata need native Windows qualification.
func (r *ReadRoot) Canonical(rel string, allowMissingLeaf bool) (string, error) {
	file, err := r.Open(rel)
	if err != nil {
		return "", err
	}
	defer file.Close()
	return file.Name(), nil
}
func (r *ReadRoot) Readlink(string, func(string) error) (string, error) { return "", ErrUnsupported }

// Exact component traversal requires the native Windows adapter qualification.
func (r *ReadRoot) OpenExact(rel string) (*os.File, error) {
	if _, err := exactReadRelative(rel); err != nil {
		return nil, err
	}
	for _, component := range strings.FieldsFunc(rel, func(r rune) bool { return r == '\\' || r == '/' }) {
		if component == ".." {
			return nil, ErrUnsupported
		}
	}
	return r.Open(rel)
}
