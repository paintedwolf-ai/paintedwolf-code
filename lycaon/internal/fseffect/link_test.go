package fseffect

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestLinkPublishesEntryWithoutFollowingReplacedTarget(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "target"), []byte("kept"), 0o600); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	loc := Location{Root: root, Rel: "link"}
	if err := Link(LinkRequest{Location: loc, Target: "target"}); err != nil {
		testutil.FailErr(t, "Link failed", err)
	}
	if got, err := os.Readlink(filepath.Join(root, "link")); err != nil || got != "target" {
		t.Fatalf("created link = %q, %v", got, err)
	}
	if err := Link(LinkRequest{Location: loc, Target: "missing"}); !errors.Is(err, os.ErrExist) {
		t.Fatalf("no-force replacement = %v", err)
	}
	if err := Link(LinkRequest{Location: loc, Target: "missing", Replace: true}); err != nil {
		testutil.FailErr(t, "Link failed", err)
	}
	if got, err := os.Readlink(filepath.Join(root, "link")); err != nil || got != "missing" {
		t.Fatalf("replaced link = %q, %v", got, err)
	}
	if got, err := os.ReadFile(filepath.Join(root, "target")); err != nil || string(got) != "kept" {
		t.Fatalf("original target = %q, %v", got, err)
	}
}

func TestHardLinkRetainsAndReplacesInodeWithoutFollowingSymlink(t *testing.T) {
	root, retainRoot := t.TempDir(), t.TempDir()
	origPath := filepath.Join(root, "a.txt")
	peerPath := filepath.Join(root, "b.txt")
	testutil.FailErr(t, "write a.txt", os.WriteFile(origPath, []byte("v1\n"), 0o600))
	testutil.FailErr(t, "link b.txt", os.Link(origPath, peerPath))

	srcLoc := Location{Root: root, Rel: "a.txt"}
	retainLoc := Location{Root: retainRoot, Rel: "sub/retained-a"}
	testutil.FailErr(t, "retain hard link", HardLink(HardLinkRequest{
		Source:   srcLoc,
		Location: retainLoc,
		DirMode:  0o700,
	}))
	if err := HardLink(HardLinkRequest{Source: srcLoc, Location: retainLoc}); !errors.Is(err, os.ErrExist) {
		t.Fatalf("expected os.ErrExist without Replace, got %v", err)
	}

	// Overwrite both a.txt and b.txt with new inodes, then restore a.txt from retained link.
	_, err := Replace(ReplaceRequest{Location: srcLoc, Mode: 0o600, Source: os.NewFile(0, "")})
	_ = err
	testutil.FailErr(t, "overwrite a.txt", os.WriteFile(origPath, []byte("v2\n"), 0o600))
	testutil.FailErr(t, "overwrite b.txt", os.WriteFile(peerPath, []byte("v2\n"), 0o600))

	testutil.FailErr(t, "restore a.txt from retained link", HardLink(HardLinkRequest{
		Source:   retainLoc,
		Location: srcLoc,
		Replace:  true,
	}))
	testutil.FailErr(t, "restore b.txt from retained link", HardLink(HardLinkRequest{
		Source:   retainLoc,
		Location: Location{Root: root, Rel: "b.txt"},
		Replace:  true,
	}))
	infoA, err := os.Lstat(origPath)
	testutil.FailErr(t, "lstat a.txt", err)
	infoB, err := os.Lstat(peerPath)
	testutil.FailErr(t, "lstat b.txt", err)
	if !os.SameFile(infoA, infoB) {
		t.Fatal("expected restored a.txt and b.txt to share retained inode")
	}

	// Refuses symlink source.
	symLoc := Location{Root: root, Rel: "sym"}
	testutil.FailErr(t, "create symlink", Link(LinkRequest{Location: symLoc, Target: "a.txt"}))
	if err := HardLink(HardLinkRequest{
		Source:   symLoc,
		Location: Location{Root: retainRoot, Rel: "bad"},
	}); !errors.Is(err, ErrSymlink) {
		t.Fatalf("expected ErrSymlink for symlink source, got %v", err)
	}
}
