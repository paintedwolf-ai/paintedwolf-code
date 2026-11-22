package projectroot_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/testutil"
)

// kernelPath walks path one component at a time the way the kernel does:
// a link's target replaces it, and ".." leaves the directory actually
// reached. A missing tail of plain names is where a create would land.
// It shares nothing with the resolver it checks.
func kernelPath(path string) (string, bool) {
	pending := strings.Split(path, "/")
	current, links := "/", 0
	for len(pending) > 0 {
		name := pending[0]
		pending = pending[1:]
		switch name {
		case "", ".":
			continue
		case "..":
			current = filepath.Dir(current)
			continue
		}
		next := filepath.Join(current, name)
		info, err := os.Lstat(next)
		if errors.Is(err, fs.ErrNotExist) {
			for _, rest := range pending {
				if rest == ".." {
					return "", false
				}
			}
			return filepath.Join(append([]string{next}, pending...)...), true
		}
		if err != nil {
			return "", false
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			if links++; links > 40 {
				return "", false
			}
			target, err := os.Readlink(next)
			if err != nil {
				return "", false
			}
			if filepath.IsAbs(target) {
				current = "/"
			}
			pending = append(strings.Split(target, "/"), pending...)
			continue
		}
		if !info.IsDir() && strings.Join(pending, "") != "" {
			return "", false
		}
		stored, ok := storedName(current, name, info)
		if !ok {
			return "", false
		}
		current = filepath.Join(current, stored)
	}
	return current, true
}

// storedNames caches entry names; the tree never changes while fuzzing.
var storedNames sync.Map

// storedName is the directory entry for name, so a case- or
// normalization-insensitive lookup still yields the name on disk.
func storedName(directory, name string, info fs.FileInfo) (string, bool) {
	key := directory + "\x00" + name
	if stored, ok := storedNames.Load(key); ok {
		return stored.(string), true
	}
	stored, ok := listedName(directory, name, info)
	if ok {
		storedNames.Store(key, stored)
	}
	return stored, ok
}

func listedName(directory, name string, info fs.FileInfo) (string, bool) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return "", false
	}
	for _, entry := range entries {
		if entry.Name() == name {
			return name, true
		}
	}
	for _, entry := range entries {
		other, err := os.Lstat(filepath.Join(directory, entry.Name()))
		if err == nil && os.SameFile(info, other) {
			return entry.Name(), true
		}
	}
	return "", false
}

func within(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// resolutionTree is a project with a nested root, links that stay inside,
// links that leave by relative and absolute targets, a loop, dangling links
// and a link into a shallower outside folder whose own links are relative.
func resolutionTree(f *testing.F) (base string, roots []projectroot.RootRef) {
	f.Helper()
	base, err := filepath.EvalSymlinks(f.TempDir())
	testutil.FailErr(f, "resolve temp dir", err)
	root := filepath.Join(base, "root")
	for _, dir := range []string{"root/sub/deep/er", "root/nested/inner", "root/deep/a", "outside/shallow"} {
		testutil.FailErr(f, "mkdir "+dir, os.MkdirAll(filepath.Join(base, dir), 0o755))
	}
	for _, file := range []string{"root/sub/file.txt", "root/nested/inner/n.txt", "outside/secret"} {
		testutil.FailErr(f, "write "+file, os.WriteFile(filepath.Join(base, file), []byte("x"), 0o600))
	}
	links := map[string]string{
		"root/in":                    "sub",
		"root/sub/deep/er/back":      "../../file.txt",
		"root/up":                    "..",
		"root/out":                   "../outside",
		"root/abs":                   filepath.Join(base, "outside"),
		"root/loop":                  "loop",
		"root/dangle":                "../outside/missing",
		"root/inner-dangle":          "sub/missing",
		"root/deep/a/b":              filepath.Join(base, "outside", "shallow"),
		"root/nested/to-parent":      "../sub",
		"outside/shallow/rel":        "../x",
		"outside/shallow/back":       "../../root/missing",
		"outside/shallow/to-root":    "../../root/sub",
		"root-alias":                 "root",
		"outside/shallow/deep-alias": "../../root/deep/a/b",
	}
	for name, target := range links {
		testutil.FailErr(f, "link "+name, os.Symlink(target, filepath.Join(base, name)))
	}
	return base, []projectroot.RootRef{
		{ID: "r1", Label: "main", Path: root, IsPrimary: true},
		{ID: "r2", Label: "nested", Path: filepath.Join(root, "nested")},
	}
}

// FuzzRootResolutionMatchesKernel requires every resolver to agree with the
// kernel's own walk: a canonical path names what the kernel reaches, and a
// path judged inside a root, or matched to one, is inside it on disk.
func FuzzRootResolutionMatchesKernel(f *testing.F) {
	base, roots := resolutionTree(f)
	for _, seed := range []string{
		"root/sub/file.txt", "root/in/deep/er/back", "root/out/secret", "root/abs/secret", "root/up/outside/secret",
		"root/loop/x", "root/dangle", "root/inner-dangle", "root/deep/a/b/rel", "root/deep/a/b/back",
		"root/deep/a/b/to-root/file.txt", "root/nested/to-parent/file.txt", "root-alias/sub/new.txt",
		"root/sub/../../outside/secret", "root/sub/file.txt/x", "@nested/inner/n.txt", "@main/../outside",
		"outside/shallow/deep-alias/rel",
	} {
		f.Add(seed, uint8(0))
	}
	f.Fuzz(func(t *testing.T, raw string, active uint8) {
		if strings.ContainsRune(raw, 0) || len(raw) > 512 {
			return
		}
		path := filepath.Join(base, raw)
		kernel, resolved := kernelPath(path)
		if canonical := fspath.CanonicalPath(path); canonical != "" && resolved && canonical != filepath.ToSlash(kernel) {
			t.Fatalf("CanonicalPath(%q) = %q; the kernel reaches %q", path, canonical, kernel)
		}
		for _, root := range roots {
			rootOnDisk, _ := kernelPath(root.Path)
			if projectroot.PathWithinRoot(root.Path, path) && resolved && !within(rootOnDisk, kernel) {
				t.Fatalf("PathWithinRoot(%q, %q) holds; the kernel reaches %q", root.Path, path, kernel)
			}
		}
		if root, rel, ok := projectroot.MatchRoot(roots, path); ok && resolved {
			rootOnDisk, _ := kernelPath(root.Path)
			if want := filepath.Join(rootOnDisk, filepath.FromSlash(rel)); want != kernel {
				t.Fatalf("MatchRoot(%q) = %s %q; the kernel reaches %q", path, root.ID, rel, kernel)
			}
		}
		abs, root, err := projectroot.ResolveAbs(roots, roots[int(active)%len(roots)].ID, raw)
		if err == nil && !within(root.Path, abs) {
			t.Fatalf("ResolveAbs(%q) = %q, outside root %q", raw, abs, root.Path)
		}
	})
}
