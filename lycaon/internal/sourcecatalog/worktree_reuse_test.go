package sourcecatalog

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/litprefilter"
	"github.com/lycaon/lycaon/internal/testutil"
)

type denyCollectionAdmission struct {
	file *os.File
}

func (a denyCollectionAdmission) DirectoryFile() *os.File      { return a.file }
func (a denyCollectionAdmission) AdmitDirectoryRead() error    { return nil }
func (a denyCollectionAdmission) AllowDirectoryCollection() bool { return false }

func TestSubrootInvalidationMappingAndCollectionRefusal(t *testing.T) {
	c, root := indexFixture(t)
	body := []byte("package sub\nconst ExactToken = 1\n")
	writeIndexFile(t, root.Path, "sub/item.go", string(body))

	subRoot := filepath.Join(root.Path, "sub")
	whole, ok := treeChangesForRoot(subRoot, root.Path, nil)
	if !ok || !slices.Equal(whole, []string{"sub"}) {
		t.Fatalf("whole subroot mapping = %v (%v), want [sub]", whole, ok)
	}
	scoped, ok := treeChangesForRoot(subRoot, root.Path, []string{
		"item.go",
		".",
		"../outside.go",
		filepath.Join(subRoot, "nested/abs.go"),
	})
	if !ok || !slices.Equal(scoped, []string{"sub/item.go", "sub", "sub/nested/abs.go"}) {
		t.Fatalf("scoped subroot mapping = %v (%v)", scoped, ok)
	}

	dirFile, err := os.Open(root.Path)
	testutil.FailErr(t, "open root dir", err)
	defer func() { _ = dirFile.Close() }()
	_, err = c.Directories.ReadDirectory(t.Context(), "p", root, ".", DirectoryRead{
		Admission: denyCollectionAdmission{file: dirFile},
	})
	if !errors.Is(err, ErrDirectoryNamespace) {
		t.Fatalf("ReadDirectory with denied collection = %v, want ErrDirectoryNamespace", err)
	}

	absFile := filepath.Join(subRoot, "item.go")
	info, err := os.Stat(absFile)
	testutil.FailErr(t, "stat item.go", err)
	version := fspath.ObserveFile(info)
	entry := Entry{Path: "sub/item.go", Size: info.Size(), Modified: info.ModTime()}

	read := c.Literals.BeginRead(root.Path, entry, version)
	if read == nil {
		t.Fatal("BeginRead returned nil")
	}
	_, err = io.ReadAll(read.Wrap(t.Context(), bytes.NewReader(body)))
	testutil.FailErr(t, "read wrapped stream", err)
	if !read.Commit() {
		t.Fatal("expected Commit to publish literal bloom")
	}
	read.Close()

	req := litprefilter.AllOf("MissingToken")
	if req.Empty() {
		t.Fatal("expected non-empty requirement")
	}
	if !c.Literals.CanPrune(root.Path, req, entry) || !c.Literals.CanPruneBytes(root.Path, req, entry, version) {
		t.Fatal("expected CanPrune and CanPruneBytes true for absent literal")
	}
	c.InvalidateRoot(subRoot, "item.go")
	if c.Literals.CanPrune(root.Path, req, entry) {
		t.Fatal("expected subroot invalidation to drop cached literal bloom")
	}
}
