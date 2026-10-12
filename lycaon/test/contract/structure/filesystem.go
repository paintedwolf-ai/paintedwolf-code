package structure

import (
	"io/fs"
	"os"
	"path/filepath"
)

// FileSystem adapts the checkout on disk to Tree.
type FileSystem struct {
	root string
}

func NewFileSystem(root string) *FileSystem { return &FileSystem{root: root} }

func (f *FileSystem) Files() ([]string, error) {
	var out []string
	err := filepath.WalkDir(f.root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", "node_modules", "vendor":
				return filepath.SkipDir
			}
			return nil
		}
		relative, err := filepath.Rel(f.root, path)
		if err != nil {
			return err
		}
		out = append(out, filepath.ToSlash(relative))
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (f *FileSystem) Read(relative string) ([]byte, error) {
	return os.ReadFile(filepath.Join(f.root, filepath.FromSlash(relative)))
}