package sandbox

import (
	"context"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func writeTree(t *testing.T, dir string, files []string) {
	t.Helper()
	for _, rel := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", rel, err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
}

func surveyFiles(t *testing.T, root string, opts SurveyOptions) []string {
	t.Helper()
	var got []string
	err := SurveyWalk(context.Background(), root, opts, func(e SurveyEntry) (SurveyAction, error) {
		if !e.IsDir {
			got = append(got, e.Rel)
		}
		return SurveyContinue, nil
	})
	if err != nil {
		t.Fatalf("SurveyWalk: %v", err)
	}
	sort.Strings(got)
	return got
}

func TestSurveyWalkPrunesUniversalSkipSetNotGitignore(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, []string{
		"src/a.go",
		"src/sub/b.go",
		"node_modules/dep/index.js",
		".gitignore",
		"ignored.env", // would be .gitignore-covered; survey has no such notion
	})

	got := surveyFiles(t, dir, SurveyOptions{IncludeHidden: true})
	want := map[string]bool{
		"src/a.go": true, "src/sub/b.go": true,
		"node_modules/dep/index.js": true,
		".gitignore":                true, "ignored.env": true,
	}
	for _, g := range got {
		if !want[g] {
			t.Fatalf("unexpected entry %q (gitignore must not be consulted); got %v", g, got)
		}
		delete(want, g)
	}
	if len(want) != 0 {
		t.Fatalf("missing expected entries %v; got %v", want, got)
	}
}

func TestSurveyWalkHiddenFiltering(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, []string{"keep.go", ".hidden/h.go", ".dotfile.go"})

	if got := surveyFiles(t, dir, SurveyOptions{IncludeHidden: false}); len(got) != 1 || got[0] != "keep.go" {
		t.Fatalf("hidden entries should be pruned, got %v", got)
	}
	got := surveyFiles(t, dir, SurveyOptions{IncludeHidden: true})
	if len(got) != 3 {
		t.Fatalf("IncludeHidden should surface dot entries, got %v", got)
	}
}

func TestSurveyWalkMaxDepth(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, []string{"top.go", "a/mid.go", "a/b/deep.go"})

	got := surveyFiles(t, dir, SurveyOptions{MaxDepth: 1})
	if len(got) != 1 || got[0] != "top.go" {
		t.Fatalf("MaxDepth 1 should visit only direct children, got %v", got)
	}
}

func TestSurveyWalkAdmitAndStop(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, []string{"keep/a.go", "skip/b.go"})

	got := surveyFiles(t, dir, SurveyOptions{Admit: func(rel, _ string, isDir bool) bool {
		return !isDir || rel != "skip"
	}})
	if len(got) != 1 || got[0] != "keep/a.go" {
		t.Fatalf("Admit should prune the skip/ subtree, got %v", got)
	}

	var seen int
	err := SurveyWalk(context.Background(), dir, SurveyOptions{}, func(e SurveyEntry) (SurveyAction, error) {
		if e.IsDir {
			return SurveyContinue, nil
		}
		seen++
		return SurveyStop, nil
	})
	if err != nil || seen != 1 {
		t.Fatalf("SurveyStop should end the walk after one file (seen=%d err=%v)", seen, err)
	}
}

func TestSurveyWalkClassifiesSymlinksWithoutFollowing(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, []string{"real/inside.go"})
	if err := os.Symlink("real", filepath.Join(dir, "link")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	var link *SurveyEntry
	descended := false
	err := SurveyWalk(context.Background(), dir, SurveyOptions{IncludeHidden: true}, func(e SurveyEntry) (SurveyAction, error) {
		entry := e
		switch {
		case e.Rel == "link":
			link = &entry
		case strings.HasPrefix(e.Rel, "link/"):
			descended = true
		}
		return SurveyContinue, nil
	})
	if err != nil {
		t.Fatalf("SurveyWalk: %v", err)
	}
	if link == nil {
		t.Fatal("the symlink must be visited as its own entry")
	}
	if !link.IsSymlink || link.IsDir {
		t.Fatalf("symlink-to-dir wants IsSymlink && !IsDir, got IsSymlink=%v IsDir=%v", link.IsSymlink, link.IsDir)
	}
	if descended {
		t.Fatal("the walk must not descend through a symlinked directory")
	}
}

func TestSurveyReadDirSingleLevel(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, []string{"a.go", "sub/b.go", "node_modules/x", ".dot"})

	entries, err := SurveyReadDir(dir, ".", SurveyOptions{IncludeHidden: false})
	if err != nil {
		t.Fatalf("SurveyReadDir: %v", err)
	}
	names := map[string]bool{}
	for _, e := range entries {
		names[e.Rel] = true
	}
	if !names["a.go"] || !names["sub"] {
		t.Fatalf("expected a.go and sub, got %v", names)
	}
	if names[".dot"] {
		t.Fatalf("hidden entries must be pruned, got %v", names)
	}
	if !names["node_modules"] {
		t.Fatalf("node_modules must remain visible at repo root, got %v", names)
	}
}

func TestSurveyReadDirExplicitSkipDirTarget(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, []string{settingsoverlay.Rel("blueprints/a.md"), settingsoverlay.Rel("verify.yaml"), "src/main.go"})

	rootEntries, err := SurveyReadDir(dir, ".", SurveyOptions{IncludeHidden: false})
	if err != nil {
		t.Fatalf("SurveyReadDir root: %v", err)
	}
	rootNames := map[string]bool{}
	for _, e := range rootEntries {
		rootNames[e.Rel] = true
	}
	if rootNames[settingsoverlay.DirName()] {
		t.Fatalf("default root listing must prune overlay entry: %v", rootNames)
	}
	if !rootNames["src"] {
		t.Fatalf("expected src at root, got %v", rootNames)
	}

	overlayDir := filepath.Join(dir, settingsoverlay.DirName())
	overlayEntries, err := SurveyReadDir(overlayDir, settingsoverlay.DirName(), SurveyOptions{IncludeHidden: false})
	if err != nil {
		t.Fatalf("SurveyReadDir overlay: %v", err)
	}
	if len(overlayEntries) == 0 {
		t.Fatalf("explicit overlay listing must return direct children, got none")
	}
	overlayNames := map[string]bool{}
	for _, e := range overlayEntries {
		overlayNames[e.Rel] = true
	}
	if !overlayNames[settingsoverlay.DirName()+"/blueprints"] || !overlayNames[settingsoverlay.DirName()+"/verify.yaml"] {
		t.Fatalf("expected overlay children, got %v", overlayNames)
	}
}

func TestSurveyReadDirHumanFilesShowsOverlayAndVCS(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, []string{
		settingsoverlay.Rel("blueprints/a.md"),
		".git/config",
		".github/workflows/ci.yml",
		"src/main.go",
	})

	entries, err := SurveyReadDir(dir, ".", HumanFilesSurveyOptions())
	if err != nil {
		t.Fatalf("SurveyReadDir: %v", err)
	}
	names := map[string]bool{}
	for _, e := range entries {
		names[e.Rel] = true
	}
	if !names[settingsoverlay.DirName()] {
		t.Fatalf("HumanFilesSurveyOptions must include overlay, got %v", names)
	}
	if !names[".github"] || !names["src"] {
		t.Fatalf("expected .github and src, got %v", names)
	}
	if !names[".git"] {
		t.Fatalf("human listing omitted .git: %v", names)
	}
}

func TestSurveyWalkIncludeEngineOverlayDescends(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, []string{settingsoverlay.Rel("blueprints/a.md"), ".git/HEAD", "keep.go"})

	got := surveyFiles(t, dir, SurveyOptions{IncludeHidden: true, IncludeEngineOverlay: true})
	want := map[string]bool{
		settingsoverlay.Rel("blueprints/a.md"): true,
		"keep.go":                              true,
	}
	for path := range want {
		found := false
		for _, g := range got {
			if g == path {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("missing %q in %v", path, got)
		}
	}
	for _, path := range got {
		if strings.HasPrefix(path, ".git") {
			t.Fatalf(".git must be pruned, got %v", got)
		}
	}
}
