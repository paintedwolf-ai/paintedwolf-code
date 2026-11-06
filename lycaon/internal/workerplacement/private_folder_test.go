//go:build darwin || linux

package workerplacement

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestReserveRefusesAFolderThatIsNotPrivateAndWritesNothing(t *testing.T) {
	cases := map[string]func(t *testing.T, path string){
		"open to others": func(t *testing.T, path string) {
			testutil.FailErr(t, "mkdir", os.Mkdir(path, 0o700))
			testutil.FailErr(t, "chmod", os.Chmod(path, 0o777))
		},
		"group access": func(t *testing.T, path string) {
			testutil.FailErr(t, "mkdir", os.Mkdir(path, 0o700))
			testutil.FailErr(t, "chmod", os.Chmod(path, 0o750))
		},
		"link to a directory": func(t *testing.T, path string) {
			testutil.FailErr(t, "link", os.Symlink(t.TempDir(), path))
		},
		"a file": func(t *testing.T, path string) {
			testutil.FailErr(t, "write", os.WriteFile(path, nil, 0o600))
		},
		"owned by someone else": func(t *testing.T, path string) {
			testutil.FailErr(t, "mkdir", os.Mkdir(path, 0o700))
			previous := folderOwnerUID
			folderOwnerUID = func() int { return previous() + 1 }
			t.Cleanup(func() { folderOwnerUID = previous })
		},
	}
	for name, arrange := range cases {
		t.Run(name, func(t *testing.T) {
			state := t.TempDir()
			path := filepath.Join(t.TempDir(), enginepaths.WorkerPlacementDirName(os.Getuid(), state))
			arrange(t, path)
			_, _, err := reserve(context.Background(), state, path, filepath.Dir(path), KindOutsideRoots)
			if !errors.Is(err, ErrFolderNotPrivate) {
				t.Fatalf("reserve err = %v", err)
			}
			if _, err := os.Lstat(filepath.Join(path, markerFileName)); err == nil {
				t.Fatal("a marker was written into the folder")
			}
			if _, err := os.Lstat(registryPath(state)); !os.IsNotExist(err) {
				t.Fatalf("the refused folder reached the registry: %v", err)
			}
		})
	}
}

func TestForgedMarkerInAForeignFolderIsNotTrusted(t *testing.T) {
	state := t.TempDir()
	path := filepath.Join(t.TempDir(), enginepaths.WorkerPlacementDirName(os.Getuid(), state))
	testutil.FailErr(t, "mkdir", os.Mkdir(path, 0o700))
	testutil.FailErr(t, "forge marker", writeMarker(path, "forged", state))
	testutil.FailErr(t, "open the folder", os.Chmod(path, 0o777))
	_, _, err := reserve(context.Background(), state, path, filepath.Dir(path), KindOutsideRoots)
	if !errors.Is(err, ErrFolderNotPrivate) {
		t.Fatalf("reserve err = %v", err)
	}
	if Reachable(Folder{ID: "forged", Path: path}) {
		t.Fatal("a folder open to others reads as reachable")
	}
	if FolderPresent(path) {
		t.Fatal("a folder open to others reads as present")
	}
}

func TestMarkerThatIsALinkIsNotFollowed(t *testing.T) {
	state := t.TempDir()
	path := filepath.Join(t.TempDir(), enginepaths.WorkerPlacementDirName(os.Getuid(), state))
	testutil.FailErr(t, "mkdir", os.Mkdir(path, 0o700))
	target := filepath.Join(t.TempDir(), "elsewhere")
	testutil.FailErr(t, "write target", os.WriteFile(target, []byte(`{"format_version":1,"id":"forged"}`), 0o600))
	testutil.FailErr(t, "link marker", os.Symlink(target, filepath.Join(path, markerFileName)))
	if _, err := readMarker(path); err == nil {
		t.Fatal("read a marker through a link")
	}
}

func TestDecideTakesTheNotClonablePathForAFolderThatIsNotPrivate(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	testutil.FailErr(t, "resolve", err)
	source := filepath.Join(base, "source")
	testutil.FailErr(t, "mkdir", os.Mkdir(source, 0o700))
	state := filepath.Join(base, "state")
	testutil.FailErr(t, "mkdir", os.Mkdir(state, 0o700))
	volume, err := volumeOf(source)
	testutil.FailErr(t, "volume", err)
	decision, err := atVolumeRoot(context.Background(), Request{
		StateRoot: state, StateBranches: filepath.Join(state, "worker-branches"),
		SourceRoots: []string{source}, AttachedRoots: []string{source},
		Probe: func(context.Context, string) (bool, error) { return true, nil },
	}, filepath.Join(state, "worker-branches"), Volume{ID: volume.ID, Root: plantedRoot(t, state)})
	testutil.FailErr(t, "decide", err)
	if decision.Clonable || decision.Reason != ReasonFolderNotPrivate || decision.Folder != nil {
		t.Fatalf("decision = %+v", decision)
	}
}

// plantedRoot stands in for a volume root where someone planted the folder's name.
func plantedRoot(t *testing.T, state string) string {
	t.Helper()
	root := t.TempDir()
	planted := filepath.Join(root, enginepaths.WorkerPlacementDirName(os.Getuid(), state))
	testutil.FailErr(t, "plant", os.Mkdir(planted, 0o700))
	testutil.FailErr(t, "open", os.Chmod(planted, 0o777))
	return root
}
