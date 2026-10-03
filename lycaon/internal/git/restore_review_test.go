package git

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/gittest"
)

func TestRestoreReviewBoundsCombinedText(t *testing.T) {
	dir := t.TempDir()
	gittest.Init(t, dir)
	body := strings.Repeat("x", sourceledger.MaxRevisionContentBytes/2+1)
	for _, path := range []string{"a.txt", "b.txt"} {
		testutil.FailErr(t, "seed committed file", os.WriteFile(filepath.Join(dir, path), []byte(body), 0o644))
	}
	gittest.Run(t, dir, "add", ".")
	gittest.Run(t, dir, "commit", "-m", "Seed files")
	for _, path := range []string{"a.txt", "b.txt"} {
		testutil.FailErr(t, "seed current file", os.WriteFile(filepath.Join(dir, path), []byte("local"), 0o644))
	}
	declined := errors.New("review only")
	err := NewManager().Restore(t.Context(), dir, GitRestoreOpts{Paths: []string{"."}, Review: func(_ context.Context, files []RestoreFile) error {
		if len(files) != 2 {
			t.Fatalf("review omitted a file: %d", len(files))
		}
		retained, omitted := 0, 0
		for _, file := range files {
			retained += len(file.Before.Bytes) + len(file.After.Bytes)
			if len(file.After.Bytes) == 0 {
				omitted++
			}
			if file.Before.Size != 5 || file.After.Size != int64(len(body)) || file.Before.SHA256 == "" || file.After.SHA256 == "" {
				t.Fatalf("review lost file identity: %s", file.Path)
			}
		}
		if retained > sourceledger.MaxRevisionContentBytes || omitted != 1 {
			t.Fatalf("unbounded review: %d retained bytes, %d omitted previews", retained, omitted)
		}
		return declined
	}})
	if !errors.Is(err, declined) {
		t.Fatalf("restore did not retain review outcome: %v", err)
	}
}

func TestRestoreReviewExpandsDirectoryAndFreezesSource(t *testing.T) {
	dir := t.TempDir()
	gittest.Init(t, dir)
	testutil.FailErr(t, "create policy folder", os.Mkdir(filepath.Join(dir, "pkg"), 0o755))
	path := filepath.Join(dir, "pkg", "AGENTS.md")
	testutil.FailErr(t, "write source instructions", os.WriteFile(path, []byte("committed\n"), 0o644))
	gittest.Run(t, dir, "add", "pkg/AGENTS.md")
	gittest.Run(t, dir, "commit", "-m", "Seed instructions")
	testutil.FailErr(t, "write pending changes", os.WriteFile(path, []byte("working\n"), 0o644))
	reviewed := false
	err := NewManager().Restore(t.Context(), dir, GitRestoreOpts{Paths: []string{"pkg"}, Review: func(_ context.Context, files []RestoreFile) error {
		reviewed = true
		if len(files) != 1 || files[0].Path != "pkg/AGENTS.md" || string(files[0].Before.Bytes) != "working\n" || string(files[0].After.Bytes) != "committed\n" {
			t.Fatalf("restore preview = %+v", files)
		}
		current, err := os.ReadFile(path)
		testutil.FailErr(t, "read while pending", err)
		if string(current) != "working\n" {
			t.Fatal("restore wrote before review")
		}
		return nil
	}})
	testutil.FailErr(t, "restore approved source", err)
	if !reviewed {
		t.Fatal("restore bypassed review")
	}
	current, err := os.ReadFile(path)
	testutil.FailErr(t, "read restored file", err)
	if string(current) != "committed\n" {
		t.Fatalf("restored content = %q", current)
	}
}

func TestRestoreReviewRejectsAndPreservesConcurrentEdits(t *testing.T) {
	for _, race := range []bool{false, true} {
		t.Run(map[bool]string{false: "rejected", true: "concurrent edit"}[race], func(t *testing.T) {
			dir := t.TempDir()
			gittest.Init(t, dir)
			path := filepath.Join(dir, "AGENTS.md")
			testutil.FailErr(t, "seed instructions", os.WriteFile(path, []byte("source\n"), 0o644))
			gittest.Run(t, dir, "add", "AGENTS.md")
			gittest.Run(t, dir, "commit", "-m", "Seed instructions")
			testutil.FailErr(t, "write changes", os.WriteFile(path, []byte("working\n"), 0o644))
			want := "working\n"
			err := NewManager().Restore(t.Context(), dir, GitRestoreOpts{Paths: []string{"AGENTS.md"}, Review: func(context.Context, []RestoreFile) error {
				if !race {
					return errors.New("declined")
				}
				want = "human edit\n"
				return os.WriteFile(path, []byte(want), 0o644)
			}})
			if err == nil {
				t.Fatal("restore ignored rejection or stale base")
			}
			current, err := os.ReadFile(path)
			testutil.FailErr(t, "read surviving bytes", err)
			if string(current) != want {
				t.Fatalf("restored over unapproved bytes: %q", current)
			}
		})
	}
}

func TestRestoreIndexPreviewUsesStoredBytes(t *testing.T) {
	dir := t.TempDir()
	gittest.Init(t, dir)
	testutil.FailErr(t, "configure checkout conversion", os.WriteFile(filepath.Join(dir, ".gitattributes"), []byte("*.md text eol=crlf\n"), 0o644))
	path := filepath.Join(dir, "AGENTS.md")
	testutil.FailErr(t, "write committed instructions", os.WriteFile(path, []byte("committed\n"), 0o644))
	gittest.Run(t, dir, "add", ".")
	gittest.Run(t, dir, "commit", "-m", "Seed instructions")
	testutil.FailErr(t, "write staged instructions", os.WriteFile(path, []byte("staged\r\n"), 0o644))
	gittest.Run(t, dir, "add", "AGENTS.md")
	reviewed := false
	err := NewManager().Restore(t.Context(), dir, GitRestoreOpts{Paths: []string{"AGENTS.md"}, Staged: true, Review: func(_ context.Context, files []RestoreFile) error {
		reviewed = true
		if len(files) != 1 || !files[0].IndexOnly || string(files[0].Before.Bytes) != "staged\n" || string(files[0].After.Bytes) != "committed\n" {
			t.Fatalf("index preview used checkout bytes: %+v", files)
		}
		return nil
	}})
	testutil.FailErr(t, "restore reviewed index", err)
	current, err := os.ReadFile(path)
	testutil.FailErr(t, "read untouched worktree", err)
	if !reviewed || string(current) != "staged\r\n" {
		t.Fatal("index restore changed worktree instructions or skipped review")
	}
}

// Permission bits beyond the executable bit follow the umask, so a file Git
// rewrites with 0664 instead of 0644 is not a change.
func TestRestoreContentRecordsGitModes(t *testing.T) {
	dir := t.TempDir()
	for name, perm := range map[string]os.FileMode{"plain": 0o664, "script": 0o775} {
		path := filepath.Join(dir, name)
		testutil.FailErr(t, "write "+name, os.WriteFile(path, []byte(name), 0o600))
		testutil.FailErr(t, "chmod "+name, os.Chmod(path, perm))
	}
	testutil.FailErr(t, "link", os.Symlink("plain", filepath.Join(dir, "link")))
	for name, want := range map[string]os.FileMode{"plain": 0o644, "script": 0o755, "link": os.ModeSymlink | 0o777} {
		content, err := readRestoreContent(filepath.Join(dir, name))
		testutil.FailErr(t, "read "+name, err)
		if content.Mode != want {
			t.Errorf("%s mode = %v, want %v", name, content.Mode, want)
		}
	}
}
