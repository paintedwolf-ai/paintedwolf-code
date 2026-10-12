package structure

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func fixtureGit(t *testing.T, root string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
}

func fixtureFile(t *testing.T, root, name, body string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
}

func TestFileSystemUsesCheckoutInventory(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	fixtureGit(t, root, "init", "--quiet")
	fixtureFile(t, root, ".gitignore", "scratch/\nlycaon/internal/ignored/\nlycaon/internal/tracked/\nlycaon-den/src-tauri/target/\n.claude/\n")
	fixtureFile(t, root, "lycaon/internal/tracked/tracked.go", "package tracked")
	fixtureFile(t, root, "lycaon/internal/deleted/deleted.go", "package deleted")
	fixtureGit(t, root, "add", "--force", "--", ".gitignore", "lycaon/internal/tracked/tracked.go", "lycaon/internal/deleted/deleted.go")
	if err := os.Remove(filepath.Join(root, "lycaon/internal/deleted/deleted.go")); err != nil {
		t.Fatalf("delete: %v", err)
	}
	fixtureFile(t, root, "lycaon/internal/new/new.go", "package new")
	fixtureFile(t, root, "lycaon/internal/ignored/broken.go", "invalid Go scratch content")
	fixtureFile(t, root, "lycaon-den/src-tauri/target/output", "artifact")
	fixtureFile(t, root, ".claude/worktrees/other/output", "artifact")
	fixtureFile(t, root, "scratch/external.go", "package external")
	if err := os.Symlink(filepath.Join(root, "scratch/external.go"), filepath.Join(root, "lycaon/internal/new/link.go")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	tree := NewFileSystem(root)
	files, err := tree.Files()
	if err != nil {
		t.Fatalf("files: %v", err)
	}
	want := []string{".gitignore", "lycaon/internal/new/new.go", "lycaon/internal/tracked/tracked.go"}
	if !slices.Equal(files, want) {
		t.Fatalf("files = %v, want %v", files, want)
	}
	graph, err := buildGraph(tree)
	if err != nil {
		t.Fatalf("build graph: %v", err)
	}
	if len(graph.Packages) != 2 {
		t.Fatalf("packages = %v", graph.Packages)
	}
	body, err := tree.Read(want[1])
	if err != nil || string(body) != "package new" {
		t.Fatalf("read = %q, %v", body, err)
	}
}

func TestFileSystemRequiresCheckout(t *testing.T) {
	t.Parallel()
	_, err := NewFileSystem(t.TempDir()).Files()
	if err == nil || !strings.Contains(err.Error(), "list working tree") {
		t.Fatalf("files error = %v", err)
	}
}
