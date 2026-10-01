package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestCollectFilesStopsAtNestedCheckouts(t *testing.T) {
	t.Parallel()
	for _, rootMarker := range []string{"directory", "file"} {
		t.Run(rootMarker, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			write := func(rel, content string) {
				t.Helper()
				path := filepath.Join(root, rel)
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatalf("create fixture directory: %v", err)
				}
				if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
					t.Fatalf("write fixture: %v", err)
				}
			}
			if rootMarker == "directory" {
				if err := os.Mkdir(filepath.Join(root, ".git"), 0o700); err != nil {
					t.Fatalf("create root repository marker: %v", err)
				}
			} else {
				write(".git", "gitdir: /unused/root-metadata\n")
			}
			write("root.go", "package fixture\n")
			write("src/kept.go", "package fixture\n")
			write(".local/kept.ts", "export {};\n")
			write("copies/clone/.git/HEAD", "ref: refs/heads/main\n")
			write("copies/clone/source.go", "package fixture\n")
			write("copies/worktree/.git", "gitdir: /unused/worktree-metadata\n")
			write("copies/worktree/source.go", "package fixture\n")

			jobs, _, err := collectFiles(options{root: root}, []string{"."})
			if err != nil {
				t.Fatalf("collect repository files: %v", err)
			}
			var paths []string
			for _, job := range jobs {
				paths = append(paths, job.rel)
			}
			want := []string{".local/kept.ts", "root.go", "src/kept.go"}
			if rootMarker == "file" {
				want = append([]string{".git"}, want...)
			}
			if !slices.Equal(paths, want) {
				t.Fatalf("collected files = %q, want %q", paths, want)
			}

			nested := filepath.Join(root, "copies", "worktree")
			jobs, _, err = collectFiles(options{root: nested}, []string{"."})
			if err != nil {
				t.Fatalf("collect selected worktree: %v", err)
			}
			if !slices.ContainsFunc(jobs, func(job fileJob) bool { return job.rel == "source.go" }) {
				t.Fatal("selected worktree source was omitted")
			}
		})
	}
}
