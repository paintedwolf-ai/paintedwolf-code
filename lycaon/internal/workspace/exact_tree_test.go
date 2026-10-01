package workspace

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestCopyTreeExactPreservesManifest(t *testing.T) {
	ctx := context.Background()
	parent := t.TempDir()
	source := filepath.Join(parent, "source")
	destination := filepath.Join(parent, "destination")
	testutil.FailErr(t, "create source", os.Mkdir(source, 0o750))
	nested := filepath.Join(source, "nested")
	testutil.FailErr(t, "create nested directory", os.Mkdir(nested, 0o710))
	filePath := filepath.Join(nested, "draft.txt")
	testutil.FailErr(t, "write source file", os.WriteFile(filePath, []byte("draft bytes"), 0o640))
	testutil.FailErr(t, "create source symlink", os.Symlink("draft.txt", filepath.Join(nested, "current")))

	wantHash, err := TreeSHA256(ctx, source)
	testutil.FailErr(t, "hash source", err)
	testutil.FailErr(t, "copy exact tree", CopyTreeExact(ctx, source, destination))
	gotHash, err := TreeSHA256(ctx, destination)
	testutil.FailErr(t, "hash destination", err)
	if gotHash != wantHash {
		t.Fatalf("destination hash = %q want %q", gotHash, wantHash)
	}
	link, err := os.Readlink(filepath.Join(destination, "nested", "current"))
	testutil.FailErr(t, "read copied symlink", err)
	if link != "draft.txt" {
		t.Fatalf("copied symlink = %q want draft.txt", link)
	}
	info, err := os.Stat(filepath.Join(destination, "nested", "draft.txt"))
	testutil.FailErr(t, "stat copied file", err)
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("copied mode = %o want 640", info.Mode().Perm())
	}
}

func TestTreeSHA256ChangesWithFileContent(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	path := filepath.Join(root, "draft.txt")
	testutil.FailErr(t, "write first content", os.WriteFile(path, []byte("first"), 0o600))
	first, err := TreeSHA256(ctx, root)
	testutil.FailErr(t, "hash first content", err)
	testutil.FailErr(t, "write second content", os.WriteFile(path, []byte("second"), 0o600))
	second, err := TreeSHA256(ctx, root)
	testutil.FailErr(t, "hash second content", err)
	if first == second {
		t.Fatal("tree hash did not change with file content")
	}
}
