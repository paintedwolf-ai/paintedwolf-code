package fspath_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/testutil"
)

type observationInfo struct {
	fs.FileInfo
	system any
}

func (i observationInfo) Sys() any { return i.system }

func observationRoot(t *testing.T, dir string, info fs.FileInfo) *fseffect.ReadRoot {
	t.Helper()
	root, err := fseffect.OpenReadRoot(dir)
	if errors.Is(err, fseffect.ErrUnsupported) {
		if observed := fspath.ObserveFile(info); observed != (fspath.FileObservation{}) {
			t.Fatalf("unsupported held metadata acquired file facts: %+v", observed)
		}
		return nil
	}
	testutil.FailErr(t, "open observation root", err)
	t.Cleanup(func() { testutil.FailErr(t, "close observation root", root.Close()) })
	return root
}

func assertFileObservation(t *testing.T, root *fseffect.ReadRoot, rel string, follow bool) fspath.FileObservation {
	t.Helper()
	abs := filepath.Join(root.Path(), rel)
	info, err := os.Lstat(abs)
	if follow {
		info, err = os.Stat(abs)
	}
	testutil.FailErr(t, "observe filesystem entry", err)
	observed := fspath.ObserveFile(info)
	held, err := root.Stat(rel, follow, nil)
	if errors.Is(err, fseffect.ErrUnsupported) {
		if observed != (fspath.FileObservation{}) {
			t.Fatalf("unsupported entry acquired file facts: %+v", observed)
		}
		return observed
	}
	testutil.FailErr(t, "observe held entry", err)
	if !held.POSIX {
		if observed != (fspath.FileObservation{}) {
			t.Fatalf("non-native entry acquired file facts: %+v", observed)
		}
		return observed
	}
	want := fspath.FileObservation{
		Identity: fspath.AllocationIdentity{Device: held.Device, Inode: held.Inode},
		Changed:  held.ChangeTime.UnixNano(), Size: held.Size, Links: held.Links, Known: true,
	}
	if observed != want || observed.Size != info.Size() {
		t.Fatalf("file observation=%+v held=%+v info size=%d", observed, want, info.Size())
	}
	return observed
}

func TestObserveFileCorrespondsToHeldMetadataAfterChanges(t *testing.T) {
	dir := t.TempDir()
	abs := filepath.Join(dir, "entry")
	testutil.FailErr(t, "write initial entry", os.WriteFile(abs, []byte("first"), 0o600))
	info, err := os.Stat(abs)
	testutil.FailErr(t, "stat initial entry", err)
	root := observationRoot(t, dir, info)
	if root == nil {
		return
	}
	before := assertFileObservation(t, root, "entry", false)
	testutil.FailErr(t, "rewrite entry", os.WriteFile(abs, []byte("longer replacement"), 0o600))
	testutil.FailErr(t, "restore modification time", os.Chtimes(abs, info.ModTime(), info.ModTime()))
	after := assertFileObservation(t, root, "entry", false)
	if before.Known && (after.Identity != before.Identity || after.Size == before.Size) {
		t.Fatalf("rewritten observation=%+v initial=%+v", after, before)
	}
}

func TestObserveFileHardLinksShareIdentityAndCount(t *testing.T) {
	dir := t.TempDir()
	abs := filepath.Join(dir, "entry")
	testutil.FailErr(t, "write linked entry", os.WriteFile(abs, []byte("source"), 0o600))
	info, err := os.Stat(abs)
	testutil.FailErr(t, "stat linked entry", err)
	root := observationRoot(t, dir, info)
	if root == nil {
		return
	}
	initial := assertFileObservation(t, root, "entry", false)
	if !initial.Known {
		return
	}
	alias := filepath.Join(dir, "alias")
	testutil.FailErr(t, "create hard link", os.Link(abs, alias))
	first := assertFileObservation(t, root, "entry", false)
	second := assertFileObservation(t, root, "alias", false)
	if first.Identity != initial.Identity || first.Identity != second.Identity || first.Links != 2 || second.Links != first.Links || first.Size != second.Size {
		t.Fatalf("hard-link observations initial=%+v first=%+v second=%+v", initial, first, second)
	}
	testutil.FailErr(t, "remove hard link", os.Remove(alias))
	remaining := assertFileObservation(t, root, "entry", false)
	if remaining.Identity != initial.Identity || remaining.Links != 1 || remaining.Size != initial.Size {
		t.Fatalf("remaining link=%+v initial=%+v", remaining, initial)
	}
}

func TestObserveFileDistinguishesLinkAndTargetFacts(t *testing.T) {
	dir := t.TempDir()
	abs := filepath.Join(dir, "entry")
	testutil.FailErr(t, "write link target", os.WriteFile(abs, []byte("target bytes"), 0o600))
	info, err := os.Stat(abs)
	testutil.FailErr(t, "stat link target", err)
	root := observationRoot(t, dir, info)
	if root == nil {
		return
	}
	target := assertFileObservation(t, root, "entry", false)
	if !target.Known {
		return
	}
	testutil.FailErr(t, "create symbolic link", os.Symlink("entry", filepath.Join(dir, "link")))
	link := assertFileObservation(t, root, "link", false)
	followed := assertFileObservation(t, root, "link", true)
	if link.Identity == target.Identity || followed.Identity != target.Identity || followed.Size != target.Size || followed.Links != target.Links {
		t.Fatalf("link=%+v target=%+v followed=%+v", link, target, followed)
	}
}

func TestObserveFileCannotInventFactsWithoutNativeMetadata(t *testing.T) {
	abs := filepath.Join(t.TempDir(), "entry")
	testutil.FailErr(t, "write entry", os.WriteFile(abs, []byte("source"), 0o600))
	info, err := os.Stat(abs)
	testutil.FailErr(t, "stat entry", err)
	for _, system := range []any{nil, struct{}{}} {
		observed := fspath.ObserveFile(observationInfo{FileInfo: info, system: system})
		if observed != (fspath.FileObservation{}) {
			t.Fatalf("unrecognized metadata %T acquired facts: %+v", system, observed)
		}
	}
}
