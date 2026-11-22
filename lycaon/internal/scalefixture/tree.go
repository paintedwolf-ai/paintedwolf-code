// Package scalefixture writes the deterministic source tree that the scale
// assertions and the benchmarks measure.
package scalefixture

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	// LargeFiles is the size of one project in the scale fixture.
	LargeFiles = 500_000
	// FilesPerDirectory and DirectoriesPerGroup fix the tree's shape.
	FilesPerDirectory   = 500
	DirectoriesPerGroup = 50
)

// Shape is what WriteTree wrote.
type Shape struct {
	Files       int   `json:"files"`
	Directories int   `json:"directories"`
	Bytes       int64 `json:"bytes"`
}

var extensions = [...]string{".go", ".ts", ".sql", ".py", ".md"}

// Path is file index's slash-separated path under the root.
func Path(index int) string {
	directory := index / FilesPerDirectory
	return fmt.Sprintf("g%03d/d%03d/f%03d%s", directory/DirectoriesPerGroup, directory%DirectoriesPerGroup,
		index%FilesPerDirectory, extensions[index%len(extensions)])
}

// Body is file index's content: 128 to 511 bytes that vary with the index.
func Body(index int) []byte {
	head := fmt.Sprintf("// scale fixture file %d\nfunc file%d() int { return %d }\n", index, index, index)
	size := 128 + (index*7919)%384
	if len(head) >= size {
		return []byte(head[:size])
	}
	return []byte(head + strings.Repeat("x", size-len(head)-1) + "\n")
}

// WriteTree writes files files under root, which must be empty or absent.
func WriteTree(root string, files int) (Shape, error) {
	if files < 1 {
		return Shape{}, fmt.Errorf("scale fixture needs at least one file, got %d", files)
	}
	shape := Shape{Files: files}
	for index := range files {
		path := filepath.Join(root, filepath.FromSlash(Path(index)))
		if index%FilesPerDirectory == 0 {
			if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
				return Shape{}, err
			}
			shape.Directories++
		}
		body := Body(index)
		if err := os.WriteFile(path, body, 0o600); err != nil {
			return Shape{}, err
		}
		shape.Bytes += int64(len(body))
	}
	return shape, nil
}
