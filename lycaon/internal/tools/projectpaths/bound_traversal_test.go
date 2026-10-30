//go:build darwin || linux

package projectpaths

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestHeldChildDirectoryPreservesParentAndAdmitsRenamedPath(t *testing.T) {
	root := t.TempDir()
	testutil.FailErr(t, "create directories", os.MkdirAll(filepath.Join(root, "old", "child"), 0700))
	reader, err := fseffect.OpenReadRoot(root)
	testutil.FailErr(t, "open root", err)
	defer reader.Close()
	file, err := reader.OpenExact("old")
	testutil.FailErr(t, "open parent", err)
	denied := errors.New("new address denied")
	var admitted string
	path := BoundPath{Resolved: Resolved{Abs: filepath.Join(root, "old"), ScopeRel: "old", DisplayPath: "old"}, exactRelative: "old", admitRead: func(actual string) error {
		admitted = actual
		if filepath.Base(filepath.Dir(actual)) == "denied" {
			return denied
		}
		return nil
	}}
	dir := &BoundDir{dir: path, root: reader, file: file}
	defer dir.Close()
	child, err := dir.OpenChildDirectory("child")
	testutil.FailErr(t, "hold child", err)
	testutil.FailErr(t, "close child", child.Close())
	if _, err := file.Stat(); err != nil {
		t.Fatalf("child closed its parent: %v", err)
	}
	testutil.FailErr(t, "rename parent", os.Rename(filepath.Join(root, "old"), filepath.Join(root, "denied")))
	if _, err := dir.OpenChildDirectory("child"); !errors.Is(err, denied) || admitted != filepath.Join(reader.Path(), "denied", "child") {
		t.Fatalf("renamed child admission=%q err=%v", admitted, err)
	}
}

func TestHeldDirectoryCanResetBorrowedMembershipObservation(t *testing.T) {
	root := t.TempDir()
	testutil.FailErr(t, "seed entry", os.WriteFile(filepath.Join(root, "entry"), nil, 0600))
	testutil.FailErr(t, "seed remaining entry", os.WriteFile(filepath.Join(root, "second"), nil, 0600))
	reader, err := fseffect.OpenReadRoot(root)
	testutil.FailErr(t, "open root", err)
	defer reader.Close()
	file, err := reader.OpenExact(".")
	testutil.FailErr(t, "open directory", err)
	dir := &BoundDir{dir: BoundPath{}, root: reader, file: file}
	if _, err := dir.DirectoryFile().Readdirnames(1); err != nil {
		t.Fatalf("borrow membership: %v", err)
	}
	testutil.FailErr(t, "reset borrowed observation", dir.ResetListing())
	names, err := dir.Names(DirectoryNames{IncludeHidden: true})
	testutil.FailErr(t, "read reset membership", err)
	if len(names) != 2 || names[0] != "entry" || names[1] != "second" {
		t.Fatalf("reset membership=%v", names)
	}
	testutil.FailErr(t, "close directory", dir.Close())
	if err := dir.ResetListing(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("reset closed directory=%v", err)
	}
}

func TestHeldDirectoryReadAdmissionTracksPhysicalAddressBetweenBatches(t *testing.T) {
	root := t.TempDir()
	testutil.FailErr(t, "create directory", os.Mkdir(filepath.Join(root, "allowed"), 0700))
	for _, name := range []string{"first", "second"} {
		testutil.FailErr(t, "seed entry", os.WriteFile(filepath.Join(root, "allowed", name), nil, 0600))
	}
	reader, err := fseffect.OpenReadRoot(root)
	testutil.FailErr(t, "open root", err)
	defer reader.Close()
	file, err := reader.OpenExact("allowed")
	testutil.FailErr(t, "open directory", err)
	denied := errors.New("physical address denied")
	var admitted string
	path := BoundPath{Resolved: Resolved{Abs: filepath.Join(root, "allowed"), ScopeRel: "allowed", DisplayPath: "allowed"}, admitRead: func(actual string) error {
		admitted = actual
		if filepath.Base(actual) == "private" {
			return denied
		}
		return nil
	}}
	dir := &BoundDir{dir: path, root: reader, file: file}
	defer dir.Close()
	testutil.FailErr(t, "admit first quantum", dir.AdmitDirectoryRead())
	if _, err := dir.DirectoryFile().Readdirnames(1); err != nil {
		t.Fatalf("first read quantum: %v", err)
	}
	testutil.FailErr(t, "move between quanta", os.Rename(filepath.Join(root, "allowed"), filepath.Join(root, "private")))
	if err := dir.AdmitDirectoryRead(); !errors.Is(err, denied) || admitted != filepath.Join(reader.Path(), "private") {
		t.Fatalf("next quantum admission=%q err=%v", admitted, err)
	}
	if names, err := dir.Names(DirectoryNames{IncludeHidden: true}); !errors.Is(err, denied) || names != nil {
		t.Fatalf("names bypassed physical floor: %v %v", names, err)
	}
}

func TestHeldDirectoryCachedNamesReadmissionAndNilFloorContainment(t *testing.T) {
	root := t.TempDir()
	testutil.FailErr(t, "create directory", os.Mkdir(filepath.Join(root, "allowed"), 0700))
	reader, err := fseffect.OpenReadRoot(root)
	testutil.FailErr(t, "open root", err)
	defer reader.Close()
	file, err := reader.OpenExact("allowed")
	testutil.FailErr(t, "open directory", err)
	denied := errors.New("read permission revoked")
	allow := true
	dir := &BoundDir{root: reader, file: file, dir: BoundPath{admitRead: func(string) error {
		if !allow {
			return denied
		}
		return nil
	}}}
	_, err = dir.Names(DirectoryNames{IncludeHidden: true})
	testutil.FailErr(t, "read original membership", err)
	allow = false
	if _, err := dir.Names(DirectoryNames{IncludeHidden: true}); !errors.Is(err, denied) {
		t.Fatalf("cached membership bypassed floor: %v", err)
	}
	dir.dir.admitRead = nil
	testutil.FailErr(t, "move outside root", os.Rename(filepath.Join(root, "allowed"), filepath.Join(t.TempDir(), "outside")))
	if err := dir.AdmitDirectoryRead(); !errors.Is(err, fseffect.ErrSymlink) {
		t.Fatalf("nil floor bypassed containment: %v", err)
	}
	testutil.FailErr(t, "close held directory", dir.Close())
	if err := dir.AdmitDirectoryRead(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("closed admission=%v", err)
	}
}

func TestNamedDirectoryFallbackRevalidatesCurrentAuthorityAndIdentity(t *testing.T) {
	root := t.TempDir()
	testutil.FailErr(t, "create directory", os.MkdirAll(filepath.Join(root, "parent", "child"), 0700))
	identity, err := directoryIdentity(root)
	testutil.FailErr(t, "capture root identity", err)
	info, err := os.Stat(root)
	testutil.FailErr(t, "capture root metadata", err)
	reader, err := fseffect.OpenReadRoot(root)
	testutil.FailErr(t, "open root", err)
	defer reader.Close()
	file, err := reader.OpenExact("parent/child")
	testutil.FailErr(t, "hold child and cached parent", err)
	dir := &BoundDir{root: reader, file: file, dir: BoundPath{rootInfo: info, rootIdentity: identity, exactRelative: "parent/child", Resolved: Resolved{Root: projectroot.RootRef{Path: root}, Abs: filepath.Join(root, "parent", "child"), ScopeRel: "parent/child", DisplayPath: "parent/child"}}}
	defer dir.Close()
	actual, err := dir.namedDirectoryPath()
	testutil.FailErr(t, "validate unchanged named directory", err)
	if actual != file.Name() {
		t.Fatalf("named authority=%q want %q", actual, file.Name())
	}
	testutil.FailErr(t, "move cached parent outside", os.Rename(filepath.Join(root, "parent"), filepath.Join(t.TempDir(), "outside")))
	testutil.FailErr(t, "replace original path", os.MkdirAll(filepath.Join(root, "parent", "child"), 0700))
	if _, err := dir.namedDirectoryPath(); !errors.Is(err, fseffect.ErrSymlink) {
		t.Fatalf("named fallback accepted moved cached parent: %v", err)
	}
	testutil.FailErr(t, "replace configured root", os.Rename(root, filepath.Join(t.TempDir(), "moved-root")))
	testutil.FailErr(t, "recreate configured root", os.MkdirAll(filepath.Join(root, "parent", "child"), 0700))
	if _, err := dir.namedDirectoryPath(); err == nil {
		t.Fatal("named fallback accepted replaced configured root")
	}
}
