package structure

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

// FileSystem reads tracked and nonignored checkout sources, matching the budget inventory.
type FileSystem struct{ root string }

func NewFileSystem(root string) *FileSystem { return &FileSystem{root: root} }

func (f *FileSystem) Files() ([]string, error) {
	raw, err := exec.Command("git", "-C", f.root, "ls-files", "--cached", "--others", "--exclude-standard", "-z").Output()
	if err != nil {
		return nil, fmt.Errorf("list working tree: %w", err)
	}
	var files []string
	for _, name := range strings.Split(string(raw), "\x00") {
		if name == "" {
			continue
		}
		info, err := os.Lstat(filepath.Join(f.root, filepath.FromSlash(name)))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("inspect %s: %w", name, err)
		}
		if info.Mode().IsRegular() {
			files = append(files, name)
		}
	}
	slices.Sort(files)
	return slices.Compact(files), nil
}

func (f *FileSystem) Read(relative string) ([]byte, error) {
	return os.ReadFile(filepath.Join(f.root, filepath.FromSlash(relative)))
}
