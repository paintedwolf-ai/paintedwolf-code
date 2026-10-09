package maintainability

import (
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

// workingTree lists the checkout's files, tracked or not, and reads them.
type workingTree struct {
	root   string
	paths  []string
	bodies map[string][]byte
}

func openWorkingTree(root string) (*workingTree, error) {
	raw, err := exec.Command("git", "-C", root, "ls-files", "--cached", "--others", "--exclude-standard", "-z").Output()
	if err != nil {
		return nil, fmt.Errorf("list working tree: %w", err)
	}
	paths := strings.Split(strings.TrimSuffix(string(raw), "\x00"), "\x00")
	slices.Sort(paths)
	return &workingTree{root: root, paths: slices.Compact(paths)}, nil
}

func (w *workingTree) files() []string { return w.paths }

func (w *workingTree) regular(relative string) bool {
	if w.bodies != nil {
		_, ok := w.bodies[relative]
		return ok
	}
	info, err := os.Lstat(filepath.Join(w.root, filepath.FromSlash(relative)))
	return err == nil && info.Mode().IsRegular()
}

func (w *workingTree) read(relative string) ([]byte, error) {
	if w.bodies != nil {
		body, ok := w.bodies[relative]
		if !ok {
			return nil, os.ErrNotExist
		}
		return body, nil
	}
	return os.ReadFile(filepath.Join(w.root, filepath.FromSlash(relative)))
}

// glob matches a slash-separated pattern against the tree's paths with the
// semantics of filepath.Glob, where a wildcard never crosses a separator.
func glob(tree *workingTree, pattern string) ([]string, error) {
	if _, err := path.Match(pattern, ""); err != nil {
		return nil, fmt.Errorf("pattern %q: %w", pattern, err)
	}
	var out []string
	for _, candidate := range tree.files() {
		if matched, _ := path.Match(pattern, candidate); matched {
			out = append(out, candidate)
		}
	}
	return out, nil
}
