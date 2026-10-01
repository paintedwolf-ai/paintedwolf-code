package check

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func RepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	FailErr(t, "get working directory", err)
	for {
		if _, err := os.Stat(filepath.Join(dir, "docs", "openapi.yaml")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("repo root not found")
		}
		dir = parent
	}
}

func ServeBuildFilePaths(t *testing.T, root string) []string {
	t.Helper()
	appDir := filepath.Join(root, "lycaon", "internal", "app")
	entries, err := os.ReadDir(appDir)
	FailErr(t, "read internal/app", err)
	var paths []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if name == "build.go" || (strings.HasPrefix(name, "build_") && strings.HasSuffix(name, ".go")) {
			paths = append(paths, filepath.Join(appDir, name))
		}
	}
	sort.Strings(paths)
	if len(paths) == 0 {
		t.Fatal("no serve build sources under internal/app")
	}
	return paths
}

func ServeWireSource(t *testing.T) string {
	t.Helper()
	root := RepoRoot(t)
	var b strings.Builder
	for _, path := range ServeBuildFilePaths(t, root) {
		data, err := os.ReadFile(path)
		FailErr(t, "read file", err)
		b.Write(data)
		b.WriteByte('\n')
	}
	return b.String()
}
