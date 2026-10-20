//go:build windows

package fseffect

import (
	"os"
	"strings"
)

// Canonical returns the native path admitted by the existing handle-relative
// reader. Link metadata needs native Windows qualification.
func (r *ReadRoot) Canonical(rel string) (string, error) {
	file, err := r.OpenExact(rel)
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

// Native metadata-only execution handles need the corresponding platform adapter.
type MetadataRoot struct{}

func OpenMetadataRoot(string) (*MetadataRoot, error)     { return nil, ErrUnsupported }
func (*MetadataRoot) Path() string                       { return "" }
func (*MetadataRoot) OpenExact(string) (*os.File, error) { return nil, ErrUnsupported }
func (*MetadataRoot) Close() error                       { return ErrUnsupported }
