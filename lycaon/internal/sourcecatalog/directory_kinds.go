package sourcecatalog

import "os"

type directoryEntry interface {
	Name() string
	IsDir() bool
	Type() os.FileMode
}

type directoryKind struct {
	name string
	kind os.FileMode
}

func (e directoryKind) Name() string      { return e.name }
func (e directoryKind) IsDir() bool       { return e.kind.IsDir() }
func (e directoryKind) Type() os.FileMode { return e.kind }

type directoryKindReader struct{ file *os.File }

// The descriptor fixes the directory; entries expose no path-based metadata lookup.
func (r *directoryKindReader) ReadDir(n int) ([]directoryEntry, error) {
	entries, err := r.file.ReadDir(n)
	kinds := make([]directoryEntry, len(entries))
	for i, entry := range entries {
		kinds[i] = directoryKind{name: entry.Name(), kind: entry.Type()}
	}
	return kinds, err
}
