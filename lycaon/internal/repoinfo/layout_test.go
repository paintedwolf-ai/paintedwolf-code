package repoinfo_test

import (
	repotest "github.com/lycaon/lycaon/internal/testsetup/repoinfo"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/repoinfo"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestAnalyzeTinyRepoListsEveryFile(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"a.go", "b.go"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("package x\n"), 0o644); err != nil {
			testutil.FailErr(t, "write file", err)
		}
	}
	p := repotest.NewProvider(t)
	brief, err := repoinfo.AwaitBrief(testutil.BoundedContext(t, 5*time.Second), p, dir)
	testutil.FailErr(t, "AwaitBrief", err)
	if brief.FileCount != 2 {
		t.Fatalf("FileCount = %d want 2", brief.FileCount)
	}
	if len(brief.Layout.Files) != 2 {
		t.Fatalf("Layout.Files = %v want 2 entries", brief.Layout.Files)
	}
	if len(brief.Layout.TopLevel) != 0 {
		t.Fatalf("Layout.TopLevel = %v want empty", brief.Layout.TopLevel)
	}
}

func TestAnalyzeSmallRepoTopLevelPromotesRootFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "src", "pkg"), 0o755); err != nil {
		testutil.FailErr(t, "mkdir", err)
	}
	files := []string{
		"README.md",
		"go.mod",
		"notes.txt",
		"src/a.go",
		"src/b.go",
		"src/pkg/c.go",
		"docs/d.md",
		"docs/e.md",
		"cmd/main.go",
	}
	for _, rel := range files {
		path := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			testutil.FailErr(t, "mkdir parent", err)
		}
		if err := os.WriteFile(path, []byte("x\n"), 0o644); err != nil {
			testutil.FailErr(t, "write file", err)
		}
	}
	p := repotest.NewProvider(t)
	brief, err := repoinfo.AwaitBrief(testutil.BoundedContext(t, 5*time.Second), p, dir)
	testutil.FailErr(t, "AwaitBrief", err)
	if brief.FileCount != len(files) {
		t.Fatalf("FileCount = %d want %d", brief.FileCount, len(files))
	}
	if len(brief.Layout.Files) != 0 {
		t.Fatalf("Layout.Files = %v want empty for small tier", brief.Layout.Files)
	}
	top := brief.Layout.TopLevel
	wantContains := []string{"cmd/", "docs/", "src/", "README.md", "go.mod"}
	for _, item := range wantContains {
		if !contains(top, item) {
			t.Fatalf("Layout.TopLevel = %v missing %q", top, item)
		}
	}
	if contains(top, "notes.txt") {
		t.Fatalf("Layout.TopLevel = %v should not promote notes.txt", top)
	}
}

func TestAnalyzeMediumRepoDirsOnly(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 60; i++ {
		rel := filepath.Join("pkg", "domain", "file"+itoa(i)+".go")
		path := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			testutil.FailErr(t, "mkdir", err)
		}
		if err := os.WriteFile(path, []byte("package x\n"), 0o644); err != nil {
			testutil.FailErr(t, "write file", err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hi\n"), 0o644); err != nil {
		testutil.FailErr(t, "write readme", err)
	}
	p := repotest.NewProvider(t)
	brief, err := repoinfo.AwaitBrief(testutil.BoundedContext(t, 5*time.Second), p, dir)
	testutil.FailErr(t, "AwaitBrief", err)
	if brief.FileCount <= 50 || brief.FileCount > 500 {
		t.Fatalf("FileCount = %d want medium tier", brief.FileCount)
	}
	for _, name := range brief.Layout.TopLevel {
		if !stringsHasSuffix(name, "/") {
			t.Fatalf("medium top-level entry %q is not a dir", name)
		}
	}
	if contains(brief.Layout.TopLevel, "README.md") {
		t.Fatalf("medium tier promoted root file: %v", brief.Layout.TopLevel)
	}
}

func TestAnalyzeLargeRepoOmitsLayout(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "tree"), 0o755); err != nil {
		testutil.FailErr(t, "mkdir tree", err)
	}
	for i := 0; i < 510; i++ {
		rel := filepath.Join("tree", itoa(i)+".txt")
		if err := os.WriteFile(filepath.Join(dir, rel), []byte("x\n"), 0o644); err != nil {
			testutil.FailErr(t, "write file", err)
		}
	}
	p := repotest.NewProvider(t)
	brief, err := repoinfo.AwaitBrief(testutil.BoundedContext(t, 5*time.Second), p, dir)
	testutil.FailErr(t, "AwaitBrief", err)
	if brief.FileCount <= 500 {
		t.Fatalf("FileCount = %d want >500", brief.FileCount)
	}
	if len(brief.Layout.Files) != 0 || len(brief.Layout.TopLevel) != 0 {
		t.Fatalf("Layout = %+v want empty for large repo", brief.Layout)
	}
}

func contains(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

func stringsHasSuffix(s, suffix string) bool {
	return len(s) >= len(suffix) && s[len(s)-len(suffix):] == suffix
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [12]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
