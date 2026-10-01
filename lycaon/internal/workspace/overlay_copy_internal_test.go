package workspace

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

// Symlink nodes must be mirrored as symlinks (not followed), including
// directory and dangling targets.
func TestMirrorTreeMirrorsSymlinks(t *testing.T) {
	src := t.TempDir()
	writePrimaryFile(t, src, "real/inside.txt", "hi")
	writePrimaryFile(t, src, "top.txt", "top")
	links := map[string]string{
		"dirlink":  "real",    // -> directory
		"filelink": "top.txt", // -> regular file
		"dangling": "nowhere", // -> missing target
	}
	for name, target := range links {
		if err := os.Symlink(target, filepath.Join(src, name)); err != nil {
			testutil.FailErr(t, "symlink "+name, err)
		}
	}

	dst := filepath.Join(t.TempDir(), "out")
	if _, err := mirrorTree(context.Background(), src, dst, mirrorOptions{
		phase: "test", tolerateChange: true,
	}); err != nil {
		testutil.FailErr(t, "mirrorTree", err)
	}
	for name, want := range links {
		info, err := os.Lstat(filepath.Join(dst, name))
		testutil.FailErr(t, "lstat "+name, err)
		if info.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("%s must be a symlink, got mode %v", name, info.Mode())
		}
		got, err := os.Readlink(filepath.Join(dst, name))
		testutil.FailErr(t, "readlink "+name, err)
		if got != want {
			t.Fatalf("%s target = %q want %q", name, got, want)
		}
	}
	// The real directory is copied as a directory; the link is not descended.
	if b, err := os.ReadFile(filepath.Join(dst, "real", "inside.txt")); err != nil || string(b) != "hi" {
		t.Fatalf("real file must be copied (b=%q err=%v)", b, err)
	}
}

func TestMirrorTreeParallelCopiesEveryFile(t *testing.T) {
	saved := workspaceMirrorConcurrency
	t.Cleanup(func() { workspaceMirrorConcurrency = saved })
	workspaceMirrorConcurrency = 4

	src := t.TempDir()
	want := map[string]string{}
	for i := 0; i < 25; i++ {
		rel := fmt.Sprintf("dir%d/file%02d.txt", i%5, i)
		body := fmt.Sprintf("content-%d", i)
		writePrimaryFile(t, src, rel, body)
		want[rel] = body
	}

	dst := filepath.Join(t.TempDir(), "out")
	if _, err := mirrorTree(context.Background(), src, dst, mirrorOptions{
		phase: "test", tolerateChange: true,
	}); err != nil {
		testutil.FailErr(t, "mirrorTree", err)
	}
	for rel, body := range want {
		got, err := os.ReadFile(filepath.Join(dst, filepath.FromSlash(rel)))
		testutil.FailErr(t, "read "+rel, err)
		if string(got) != body {
			t.Fatalf("%s = %q want %q", rel, got, body)
		}
	}
}

func TestValidateCompleteMirrorRejectsSkippedPaths(t *testing.T) {
	if err := validateCompleteMirror(mirrorResult{}); err != nil {
		t.Fatalf("empty result error = %v", err)
	}
	if err := validateCompleteMirror(mirrorResult{SkippedPaths: []string{"changed.go"}}); err == nil {
		t.Fatal("skipped path accepted as a complete snapshot")
	}
}
