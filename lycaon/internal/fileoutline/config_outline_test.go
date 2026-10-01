package fileoutline_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/fileoutline"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestBuildPackageJSONNestedDependencyKeys(t *testing.T) {
	dir := t.TempDir()
	src := `{
  "name": "demo",
  "scripts": {"test": "vitest"},
  "dependencies": {"solid-js": "^1.0.0", "vite": "^5.0.0"},
  "devDependencies": {"typescript": "^5.0.0"}
}`
	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(dir, "package.json"), []byte(src), 0o644))
	out, err := fileoutline.Build(context.Background(), dir, "package.json")
	testutil.FailErr(t, "Build", err)
	names := map[string]bool{}
	for _, s := range out.Symbols {
		names[s.Name] = true
	}
	for _, want := range []string{
		"name", "scripts", "dependencies", "devDependencies",
		"dependencies.solid-js", "dependencies.vite", "devDependencies.typescript",
	} {
		if !names[want] {
			t.Fatalf("missing symbol %q in %v", want, names)
		}
	}
}

func TestBuildYAMLTopLevelAndNestedKeys(t *testing.T) {
	dir := t.TempDir()
	src := "name: demo\nlogging:\n  level: info\n  format: json\n"
	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(src), 0o644))
	out, err := fileoutline.Build(context.Background(), dir, "config.yaml")
	testutil.FailErr(t, "Build", err)
	names := map[string]bool{}
	for _, s := range out.Symbols {
		names[s.Name] = true
	}
	for _, want := range []string{"name", "logging", "logging.level", "logging.format"} {
		if !names[want] {
			t.Fatalf("missing symbol %q in %v", want, names)
		}
	}
}

func TestAnalyzeTextSmallGoSnippet(t *testing.T) {
	src := []byte("package main\n\nfunc Hello() {}\n")
	out := fileoutline.AnalyzeText(context.Background(), "snippet.go", src)
	if len(out.Symbols) == 0 {
		t.Fatalf("expected symbols for go snippet, got %+v", out)
	}
}
