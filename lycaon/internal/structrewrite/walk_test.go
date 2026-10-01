package structrewrite

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/repomap"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestWalkSearchMultiFile(t *testing.T) {
	dir := t.TempDir()
	writeWalkFile(t, dir, "a.go", "package a\n\nfunc f() { fmt.Println(\"x\") }\n")
	writeWalkFile(t, dir, "b.go", "package b\n\nfunc g() { fmt.Println(\"y\") }\n")
	writeWalkFile(t, dir, "c.txt", "fmt.Println(\"z\")\n")

	files, truncated, err := WalkSearch(t.Context(), WalkRequest{
		Root:    dir,
		Pattern: `fmt.Println($A)`,
	})
	testutil.FailErr(t, "WalkSearch", err)
	if truncated {
		t.Fatal("unexpected truncated")
	}
	if len(files) != 2 {
		t.Fatalf("file_count = %d want 2", len(files))
	}
	total := 0
	for _, f := range files {
		total += len(f.Matches)
	}
	if total != 2 {
		t.Fatalf("total_matches = %d want 2", total)
	}
}

func TestWalkSearchDecodesSelfIdentifyingEncodings(t *testing.T) {
	for _, encoding := range testutil.SelfIdentifyingTextEncodings() {
		t.Run(encoding, func(t *testing.T) {
			dir := t.TempDir()
			raw := testutil.EncodeTextFixture(t, "package main\n\nfunc main() { fmt.Println(1) }\n", encoding)
			testutil.FailErr(t, "write fixture", os.WriteFile(filepath.Join(dir, "main.go"), raw, 0o644))
			files, truncated, err := WalkSearch(t.Context(), WalkRequest{Root: dir, Pattern: `fmt.Println($A)`})
			testutil.FailErr(t, "walk search", err)
			if truncated || len(files) != 1 || len(files[0].Matches) != 1 {
				t.Fatalf("files = %+v truncated=%v", files, truncated)
			}
		})
	}
}

func TestWalkSearchMaxMatchesTruncated(t *testing.T) {
	dir := t.TempDir()
	src := "package main\n\nfunc main() {\n\tfmt.Println(\"a\")\n\tfmt.Println(\"b\")\n\tfmt.Println(\"c\")\n}\n"
	writeWalkFile(t, dir, "one.go", src)

	files, truncated, err := WalkSearch(t.Context(), WalkRequest{
		Root:       dir,
		Pattern:    `fmt.Println($A)`,
		MaxMatches: 2,
	})
	testutil.FailErr(t, "WalkSearch", err)
	if !truncated {
		t.Fatal("expected truncated")
	}
	n := 0
	for _, f := range files {
		n += len(f.Matches)
	}
	if n != 2 {
		t.Fatalf("matches = %d want 2", n)
	}
}

func TestWalkSearchSkipsUnsupportedExtension(t *testing.T) {
	dir := t.TempDir()
	writeWalkFile(t, dir, "data.qzx9", "fmt.Println(\"x\")\n")
	writeWalkFile(t, dir, "ok.go", "package main\n\nfunc main() { fmt.Println(1) }\n")

	files, _, err := WalkSearch(t.Context(), WalkRequest{Root: dir, Pattern: `fmt.Println($A)`})
	testutil.FailErr(t, "WalkSearch", err)
	if len(files) != 1 || files[0].Path != "ok.go" {
		t.Fatalf("files = %+v want only ok.go", files)
	}
}

func TestWalkSearchSubpathNonRecursive(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		testutil.FailErr(t, "mkdir", err)
	}
	writeWalkFile(t, dir, "root.go", "package root\n\nfunc r() { foo(1) }\n")
	writeWalkFile(t, dir, "sub/nested.go", "package sub\n\nfunc s() { foo(2) }\n")

	files, _, err := WalkSearch(t.Context(), WalkRequest{
		Root:      dir,
		Subpaths:  []string{"."},
		Recursive: false,
		Pattern:   `foo($A)`,
	})
	testutil.FailErr(t, "WalkSearch", err)
	if len(files) != 1 || files[0].Path != "root.go" {
		t.Fatalf("non-recursive root = %+v", files)
	}
}

func TestWalkSearchFilesCap(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 5; i++ {
		writeWalkFile(t, dir, fmt.Sprintf("f%d.go", i), "package p\n\nfunc f() { foo(1) }\n")
	}
	_, truncated, err := WalkSearch(t.Context(), WalkRequest{
		Root:     dir,
		Pattern:  `foo($A)`,
		MaxFiles: 2,
	})
	testutil.FailErr(t, "WalkSearch", err)
	if !truncated {
		t.Fatal("expected files cap truncated")
	}
}

func writeWalkFile(t *testing.T, dir, name, content string) {
	t.Helper()
	full := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		testutil.FailErr(t, "mkdir", err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		testutil.FailErr(t, "write", err)
	}
}

func TestRepomapWalkSourceFilesVisitsGoFiles(t *testing.T) {
	dir := t.TempDir()
	writeWalkFile(t, dir, "a.go", "package a\n")
	writeWalkFile(t, dir, "b.go", "package b\n")
	var n int
	_, err := repomap.WalkSourceFiles(t.Context(), repomap.SourceWalkOptions{
		Root: dir, Recursive: true,
	}, func(rel, _ string) error {
		if filepath.Ext(rel) == ".go" {
			n++
		}
		return nil
	})
	testutil.FailErr(t, "WalkSourceFiles", err)
	if n != 2 {
		t.Fatalf("visited %d go files", n)
	}
}
