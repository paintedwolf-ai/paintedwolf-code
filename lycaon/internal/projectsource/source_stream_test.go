package projectsource

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/textfile"
)

func TestSourceStreamPinsRevisionAndRejectsReplacement(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "source.txt")
	testutil.FailErr(t, "write initial source", os.WriteFile(path, []byte("original"), 0o600))
	p := &Project{ID: "project", Roots: []Root{{ID: "root", Path: root, IsPrimary: true}}}
	stream, err := OpenProjectSourceStream(p, SourceReadRequest{RootID: "root", Path: "source.txt"})
	testutil.FailErr(t, "open pinned source", err)
	defer stream.Close()
	if !stream.Current() || stream.Encoding != textfile.UTF8 {
		t.Fatal("new stream does not identify current text")
	}
	replacement := filepath.Join(root, "replacement")
	testutil.FailErr(t, "write replacement", os.WriteFile(replacement, []byte("replaced"), 0o600))
	testutil.FailErr(t, "replace source", os.Rename(replacement, path))
	if stream.Current() {
		t.Fatal("replacement retained the old revision identity")
	}
	original, err := io.ReadAll(stream.File)
	testutil.FailErr(t, "read pinned descriptor", err)
	if string(original) != "original" {
		t.Fatal("replacement changed pinned snapshot bytes")
	}
}

func TestSourceStreamCannotFollowSymlinkOutsideRoot(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	testutil.FailErr(t, "write outside file", os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("outside"), 0o600))
	testutil.FailErr(t, "create escape link", os.Symlink(outside, filepath.Join(root, "escape")))
	p := &Project{ID: "project", Roots: []Root{{ID: "root", Path: root, IsPrimary: true}}}
	stream, err := OpenProjectSourceStream(p, SourceReadRequest{RootID: "root", Path: "escape/secret.txt"})
	if err == nil {
		stream.Close()
		t.Fatal("source stream escaped its attached root")
	}
}

func TestSourceStreamRejectsInPlaceRewriteWithRestoredModificationTime(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "source.txt")
	testutil.FailErr(t, "write original", os.WriteFile(path, []byte("original"), 0o600))
	p := &Project{ID: "project", Roots: []Root{{ID: "root", Path: root, IsPrimary: true}}}
	stream, err := OpenProjectSourceStream(p, SourceReadRequest{RootID: "root", Path: "source.txt"})
	testutil.FailErr(t, "open original revision", err)
	defer stream.Close()
	testutil.FailErr(t, "rewrite equal length", os.WriteFile(path, []byte("modified"), 0o600))
	testutil.FailErr(t, "restore modification time", os.Chtimes(path, stream.info.ModTime(), stream.info.ModTime()))
	if stream.Current() {
		t.Fatal("equal-length rewrite retained an obsolete revision")
	}
}
