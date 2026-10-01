package sourcescope

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testutil"
)

func writeFile(t *testing.T, root, rel, content string) {
	t.Helper()
	abs := filepath.Join(root, filepath.FromSlash(rel))
	testutil.FailErr(t, "mkdir "+rel, os.MkdirAll(filepath.Dir(abs), 0o755))
	testutil.FailErr(t, "write "+rel, os.WriteFile(abs, []byte(content), 0o644))
}

func capturePlane() Plane {
	return Plane{IgnoreFiles: true, Budgets: sandbox.SurveyBudgets{DirectoryEntries: 1000, SubtreeEntries: 10000, WalkEntries: 100000}}
}

func surveyed(t *testing.T, root string, scope *Scope) ([]string, []sandbox.SurveyBoundary) {
	t.Helper()
	var files []string
	var boundaries []sandbox.SurveyBoundary
	opts := scope.SurveyOptions(sandbox.SurveyOptions{IncludeHidden: true})
	opts.OnBoundary = func(b sandbox.SurveyBoundary) { boundaries = append(boundaries, b) }
	err := sandbox.SurveyWalk(context.Background(), root, opts, func(e sandbox.SurveyEntry) (sandbox.SurveyAction, error) {
		if !e.IsDir {
			files = append(files, e.Rel)
		}
		return sandbox.SurveyContinue, nil
	})
	testutil.FailErr(t, "survey", err)
	sort.Strings(files)
	return files, boundaries
}

func boundaryByRel(boundaries []sandbox.SurveyBoundary, rel string) (sandbox.SurveyBoundary, bool) {
	for _, b := range boundaries {
		if b.Rel == rel {
			return b, true
		}
	}
	return sandbox.SurveyBoundary{}, false
}

func TestIgnoreFilesPruneDirectoriesBeforeTheyAreEntered(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, ".gitignore", "scratch/\n*.log\n!keep.log\n")
	writeFile(t, root, "src/main.go", "package main\n")
	writeFile(t, root, "src/debug.log", "")
	writeFile(t, root, "src/keep.log", "")
	writeFile(t, root, "scratch/huge/1", "")
	writeFile(t, root, "scratch/huge/2", "")

	scope := New(root, Options{Plane: capturePlane()})
	files, boundaries := surveyed(t, root, scope)
	want := []string{".gitignore", "src/keep.log", "src/main.go"}
	if len(files) != len(want) {
		t.Fatalf("files = %v, want %v", files, want)
	}
	for i := range want {
		if files[i] != want[i] {
			t.Fatalf("files = %v, want %v", files, want)
		}
	}
	b, ok := boundaryByRel(boundaries, "scratch")
	if !ok || b.Reason != sandbox.BoundaryScope || b.Detail != ReasonIgnored {
		t.Fatalf("scratch boundary = %+v (found=%v), want scope/ignored", b, ok)
	}
}

func TestNestedIgnoreFilesApplyWithinTheirDirectory(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "lib/.gitignore", "build/\n")
	writeFile(t, root, "lib/build/out.o", "")
	writeFile(t, root, "lib/src.go", "")
	writeFile(t, root, "app/build/keep.go", "")

	scope := New(root, Options{Plane: capturePlane()})
	files, _ := surveyed(t, root, scope)
	if contains(files, "lib/build/out.o") {
		t.Fatalf("lib/.gitignore must prune lib/build: %v", files)
	}
	if !contains(files, "app/build/keep.go") {
		t.Fatalf("a nested ignore file does not reach a sibling tree: %v", files)
	}
}

func TestRepositoryLocalExcludeIsReadAsAFileInTheTree(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, ".git/info/exclude", "local-only/\n")
	writeFile(t, root, "local-only/x", "")
	writeFile(t, root, "src/a.go", "")

	scope := New(root, Options{Plane: capturePlane()})
	files, _ := surveyed(t, root, scope)
	if contains(files, "local-only/x") || !contains(files, "src/a.go") {
		t.Fatalf("files = %v", files)
	}
}

func TestFloorAndDeclarationsOrderTheLayers(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, ".gitignore", "generated/\n")
	writeFile(t, root, "generated/api.go", "")
	writeFile(t, root, "node_modules/dep/index.js", "")
	writeFile(t, root, "fixtures/big/blob", "")
	writeFile(t, root, "src/a.go", "")

	declared := Declared{Include: []string{"generated/"}, Exclude: []string{"fixtures/big/", "node_modules/"}}
	scope := New(root, Options{Plane: capturePlane(), Floor: []string{"node_modules", "bin/Debug"}, Declared: declared})
	files, boundaries := surveyed(t, root, scope)
	if !contains(files, "generated/api.go") {
		t.Fatalf("a declared include admits past an ignore file: %v", files)
	}
	if contains(files, "fixtures/big/blob") {
		t.Fatalf("a declared exclude leaves the subtree out: %v", files)
	}
	nm, ok := boundaryByRel(boundaries, "node_modules")
	if !ok || nm.Detail != ReasonFloor {
		t.Fatalf("the floor decides before declarations; boundary = %+v", nm)
	}
	fb, ok := boundaryByRel(boundaries, "fixtures/big")
	if !ok || fb.Detail != ReasonDeclared {
		t.Fatalf("fixtures/big boundary = %+v", fb)
	}
	if _, prune := scope.PruneDir("proj/bin/Debug", ""); !prune {
		t.Fatal("a floor pattern with a slash matches at any depth")
	}
}

func TestAdmitPathChecksEveryAncestor(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, ".gitignore", "out/\n*.tmp\n")
	writeFile(t, root, "out/deep/file.go", "")
	writeFile(t, root, "src/a.go", "")

	scope := New(root, Options{Plane: capturePlane()})
	if scope.AdmitPath("out/deep/file.go", false) {
		t.Fatal("a file under an ignored directory is out of scope")
	}
	if scope.AdmitPath("src/notes.tmp", false) {
		t.Fatal("an ignored file pattern applies to the file itself")
	}
	if !scope.AdmitPath("src/a.go", false) || !scope.AdmitPath("src", true) {
		t.Fatal("admitted paths report in scope")
	}
}

func TestCatalogScopeReadsNoIgnoreFiles(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, ".gitignore", "vendor/\n")
	writeFile(t, root, "vendor/dep.go", "")

	scope := New(root, Options{Plane: Plane{Budgets: capturePlane().Budgets}})
	files, boundaries := surveyed(t, root, scope)
	if !contains(files, "vendor/dep.go") {
		t.Fatalf("an index for agent tools sees the whole tree: %v", files)
	}
	if len(boundaries) != 0 {
		t.Fatalf("no boundaries expected, got %+v", boundaries)
	}
}

func TestProviderAppliesDeclarationsOnlyWhenTheProjectIsTrusted(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, settingsoverlay.Rel("source-scope.yaml"), "version: 1\nexclude: [\"data/\"]\nbudgets:\n  capture:\n    walk_entries: 4000000\n")
	writeFile(t, root, "data/blob", "")
	cfg, err := DefaultConfig()
	testutil.FailErr(t, "default config", err)

	untrusted, err := NewProvider(cfg, func(context.Context, string) bool { return false })
	testutil.FailErr(t, "provider", err)
	if _, prune := untrusted.Capture(context.Background(), root).PruneDir("data", ""); prune {
		t.Fatal("an untrusted project's declarations must not apply")
	}
	trusted, err := NewProvider(cfg, func(context.Context, string) bool { return true })
	testutil.FailErr(t, "provider", err)
	scope := trusted.Capture(context.Background(), root)
	if _, prune := scope.PruneDir("data", ""); !prune {
		t.Fatal("a trusted project's exclude applies")
	}
	if got := scope.Budgets().WalkEntries; got != 4000000 {
		t.Fatalf("project budget = %d, want 4000000", got)
	}
	if got := trusted.Catalog(context.Background(), root).Budgets().WalkEntries; got != cfg.Catalog.Budgets.WalkEntries {
		t.Fatalf("catalog budget = %d, want the device value %d", got, cfg.Catalog.Budgets.WalkEntries)
	}
}

func TestDeclaredOverlayRejectsUnknownKeysAndVersions(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, settingsoverlay.Rel("source-scope.yaml"), "version: 2\n")
	if _, err := LoadDeclared(root); err == nil {
		t.Fatal("an unsupported version is an error, not a silent skip")
	}
	writeFile(t, root, settingsoverlay.Rel("source-scope.yaml"), "roots: [x]\n")
	if _, err := LoadDeclared(root); err == nil {
		t.Fatal("an unknown key is an error")
	}
	if declared, err := LoadDeclared(t.TempDir()); err != nil || !declared.Empty() {
		t.Fatalf("a missing overlay declares nothing: %+v %v", declared, err)
	}
}

func TestDeviceOverlayLayersOverTheBundledScope(t *testing.T) {
	overlay := filepath.Join(t.TempDir(), "source-scope.yaml")
	testutil.FailErr(t, "write overlay", os.WriteFile(overlay, []byte("budgets:\n  catalog:\n    walk_entries: 750000\n"), 0o600))
	cfg, err := LoadConfig(overlay)
	testutil.FailErr(t, "load config", err)
	bundled, err := DefaultConfig()
	testutil.FailErr(t, "default config", err)
	if cfg.Catalog.Budgets.WalkEntries != 750000 || cfg.Capture.Budgets.WalkEntries != bundled.Capture.Budgets.WalkEntries {
		t.Fatalf("config = %+v", cfg)
	}
	if !cfg.Capture.IgnoreFiles || cfg.Catalog.IgnoreFiles {
		t.Fatalf("capture honours ignore files and catalog never does: %+v", cfg)
	}
	testutil.FailErr(t, "write bad overlay", os.WriteFile(overlay, []byte("budgets:\n  catalog:\n    subtree_entries: 9000000\n    walk_entries: 750000\n"), 0o600))
	if _, err := LoadConfig(overlay); err == nil {
		t.Fatal("a subtree cap above the walk budget is rejected")
	}
}

func contains(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}

func TestDefaultHostDiscoveryHasNoRepositorySizeCeiling(t *testing.T) {
	cfg, err := DefaultConfig()
	testutil.FailErr(t, "read host discovery policy", err)
	if cfg.Capture.Budgets.Bounded() || cfg.Catalog.Budgets.Bounded() {
		t.Fatal("default discovery excludes files because a repository is large")
	}
}
