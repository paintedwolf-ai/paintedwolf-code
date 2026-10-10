package projectsource

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestCreateProjectSourceEntryFileRoundTrip(t *testing.T) {
	t.Parallel()
	p, root := writeTestProject(t, nil)

	rel, err := createProjectSourceEntry(p, SourceEntryCreateRequest{
		Path: "src/hello.go",
		Kind: SourceEntryFile,
	})
	testutil.FailErr(t, "create", err)
	if rel != "src/hello.go" {
		t.Fatalf("path = %q", rel)
	}
	info, err := os.Stat(filepath.Join(root, "src", "hello.go"))
	testutil.FailErr(t, "stat", err)
	if info.Size() != 0 {
		t.Fatalf("new file is not empty: %d bytes", info.Size())
	}

	// A new file supplies a base hash for its first write.
	read, err := ReadProjectSource(p, SourceReadRequest{Path: "src/hello.go"})
	testutil.FailErr(t, "read", err)
	if read.SHA256 == "" {
		t.Fatal("read of a new file reported no sha256 to save against")
	}
	if _, err := writeProjectSource(p, SourceWriteRequest{
		Path:       "src/hello.go",
		Content:    "package main\n",
		Encoding:   read.Encoding,
		BaseSHA256: read.SHA256,
	}); err != nil {
		t.Fatalf("save after create: %v", err)
	}
	onDisk, err := os.ReadFile(filepath.Join(root, "src", "hello.go"))
	testutil.FailErr(t, "read back", err)
	if string(onDisk) != "package main\n" {
		t.Fatalf("on disk = %q", onDisk)
	}
}

func TestCreateProjectSourceEntryFolder(t *testing.T) {
	t.Parallel()
	p, root := writeTestProject(t, nil)

	rel, err := createProjectSourceEntry(p, SourceEntryCreateRequest{
		Path: "pkg/inner",
		Kind: SourceEntryFolder,
	})
	testutil.FailErr(t, "create folder", err)
	if rel != "pkg/inner" {
		t.Fatalf("path = %q", rel)
	}
	info, err := os.Stat(filepath.Join(root, "pkg", "inner"))
	testutil.FailErr(t, "stat", err)
	if !info.IsDir() {
		t.Fatal("created entry is not a directory")
	}

	// The new folder is browsable and can immediately take a file.
	listing, err := BrowseProjectSource(p, "", "pkg/inner")
	testutil.FailErr(t, "browse", err)
	if len(listing.Entries) != 0 {
		t.Fatalf("new folder is not empty: %+v", listing.Entries)
	}
	if _, err := createProjectSourceEntry(p, SourceEntryCreateRequest{
		Path: "pkg/inner/x.go",
		Kind: SourceEntryFile,
	}); err != nil {
		t.Fatalf("create in new folder: %v", err)
	}
}

func TestCreateProjectSourceEntryMakesParentFolders(t *testing.T) {
	t.Parallel()
	p, root := writeTestProject(t, nil)

	_, err := createProjectSourceEntry(p, SourceEntryCreateRequest{
		Path: "a/b/c/deep.txt",
		Kind: SourceEntryFile,
	})
	testutil.FailErr(t, "create", err)

	if _, err := os.Stat(filepath.Join(root, "a", "b", "c", "deep.txt")); err != nil {
		t.Fatalf("nested file missing: %v", err)
	}
}

func TestCreateProjectSourceEntryRefusesExisting(t *testing.T) {
	t.Parallel()
	p, root := writeTestProject(t, map[string]string{"src/hello.go": "package main\n"})

	// Neither kind may overwrite a file or adopt a folder that is already there.
	for _, kind := range []SourceEntryKind{SourceEntryFile, SourceEntryFolder} {
		for _, path := range []string{"src/hello.go", "src"} {
			_, err := createProjectSourceEntry(p, SourceEntryCreateRequest{Path: path, Kind: kind})
			if !errors.Is(err, ErrSourceExists) {
				t.Fatalf("%s %q err = %v, want ErrSourceExists", kind, path, err)
			}
		}
	}
	onDisk, err := os.ReadFile(filepath.Join(root, "src", "hello.go"))
	testutil.FailErr(t, "read back", err)
	if string(onDisk) != "package main\n" {
		t.Fatalf("existing file was clobbered: %q", onDisk)
	}
}

func TestCreateProjectSourceEntryJail(t *testing.T) {
	t.Parallel()
	p, _ := writeTestProject(t, nil)

	for _, kind := range []SourceEntryKind{SourceEntryFile, SourceEntryFolder} {
		for _, path := range []string{"../escape", "src/../../escape"} {
			_, err := createProjectSourceEntry(p, SourceEntryCreateRequest{Path: path, Kind: kind})
			if !errors.Is(err, ErrSourcePathDenied) {
				t.Fatalf("%s %q err = %v, want ErrSourcePathDenied", kind, path, err)
			}
		}
		for _, path := range []string{"", "   "} {
			_, err := createProjectSourceEntry(p, SourceEntryCreateRequest{Path: path, Kind: kind})
			if !errors.Is(err, ErrSourcePathInvalid) {
				t.Fatalf("%s empty path err = %v, want ErrSourcePathInvalid", kind, err)
			}
		}
	}
}

func TestCreateProjectSourceEntryKindRequired(t *testing.T) {
	t.Parallel()
	p, root := writeTestProject(t, nil)

	for _, kind := range []SourceEntryKind{"", "symlink", "FILE"} {
		if _, err := createProjectSourceEntry(p, SourceEntryCreateRequest{
			Path: "new.txt",
			Kind: kind,
		}); !errors.Is(err, ErrSourceKindInvalid) {
			t.Fatalf("kind %q err = %v, want ErrSourceKindInvalid", kind, err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "new.txt")); !os.IsNotExist(err) {
		t.Fatalf("refused create left something behind: %v", err)
	}
}

func TestCreateProjectSourceEntryUnknownRoot(t *testing.T) {
	t.Parallel()
	p, _ := writeTestProject(t, nil)

	if _, err := createProjectSourceEntry(p, SourceEntryCreateRequest{
		Path:   "new.txt",
		Kind:   SourceEntryFile,
		RootID: "nope",
	}); !errors.Is(err, ErrSourceNotFound) {
		t.Fatalf("err = %v, want ErrSourceNotFound", err)
	}
}

func TestCreateProjectSourceEntryPinsSelectedRoot(t *testing.T) {
	t.Parallel()
	primary := t.TempDir()
	secondary := t.TempDir()
	p := &Project{
		ID: "p1",
		Roots: []Root{
			{ID: "r1", Path: primary, IsPrimary: true},
			{ID: "r2", Path: secondary},
		},
	}

	_, err := createProjectSourceEntry(p, SourceEntryCreateRequest{
		Path:   "only-here.txt",
		Kind:   SourceEntryFile,
		RootID: "r2",
	})
	testutil.FailErr(t, "create", err)

	if _, err := os.Stat(filepath.Join(secondary, "only-here.txt")); err != nil {
		t.Fatalf("file missing from selected root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(primary, "only-here.txt")); !os.IsNotExist(err) {
		t.Fatalf("file leaked into the primary root: %v", err)
	}
}

func TestCreateProjectSourceEntryNoRoots(t *testing.T) {
	t.Parallel()
	for _, kind := range []SourceEntryKind{SourceEntryFile, SourceEntryFolder} {
		if _, err := createProjectSourceEntry(&Project{ID: "p1"}, SourceEntryCreateRequest{
			Path: "new",
			Kind: kind,
		}); !errors.Is(err, ErrSourceNoRoot) {
			t.Fatalf("%s err = %v, want ErrSourceNoRoot", kind, err)
		}
	}
}
