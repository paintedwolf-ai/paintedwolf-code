package fspath_test

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestCanonicalPathCollapsesEverySpellingOfOneDirectory(t *testing.T) {
	home, err := os.UserHomeDir()
	testutil.FailErr(t, "home dir", err)

	for _, target := range []string{home, os.TempDir(), "/etc"} {
		if _, err := os.Stat(target); err != nil {
			continue
		}
		want := fspath.CanonicalPath(target)
		if want == "" {
			t.Fatalf("CanonicalPath(%q) = empty", target)
		}
		aliases := testutil.AliasSpellings(t, target)
		if len(aliases) == 0 {
			t.Logf("%s: host supports no alias spellings", target)
		}
		for name, alias := range aliases {
			if got := fspath.CanonicalPath(alias); got != want {
				t.Errorf("%s via %s: CanonicalPath(%q) = %q, want %q — the same directory reduced to two strings",
					target, name, alias, got, want)
			}
		}
	}
}

func TestCanonicalPathResolvesASymlinkToItsTarget(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	testutil.FailErr(t, "mkdir target", os.Mkdir(target, 0o755))
	link := filepath.Join(dir, "link")
	testutil.FailErr(t, "symlink", os.Symlink(target, link))

	if got, want := fspath.CanonicalPath(link), fspath.CanonicalPath(target); got != want {
		t.Fatalf("CanonicalPath(symlink) = %q, want %q", got, want)
	}
}

func TestCanonicalPathCarriesAMissingTail(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "not-created-yet", "leaf.txt")
	got := fspath.CanonicalPath(missing)
	wantPrefix := fspath.CanonicalPath(dir)
	if wantPrefix == "" || got == "" {
		t.Fatalf("CanonicalPath returned empty: dir=%q leaf=%q", wantPrefix, got)
	}
	if !strings.HasPrefix(got, wantPrefix+"/") {
		t.Fatalf("CanonicalPath(%q) = %q, want it under %q", missing, got, wantPrefix)
	}
	if filepath.Base(got) != "leaf.txt" {
		t.Fatalf("CanonicalPath(%q) = %q, want the missing tail preserved", missing, got)
	}
}

func TestCanonicalPathDoesNotBlockOnAFIFO(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "pipe")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("cannot create a FIFO here: %v", err)
	}
	done := make(chan string, 1)
	go func() { done <- fspath.CanonicalPath(fifo) }()
	select {
	case got := <-done:
		if got == "" {
			t.Fatalf("CanonicalPath(fifo) = empty")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("CanonicalPath blocked on a FIFO — O_NONBLOCK is missing")
	}
}

func TestCanonicalPathHandlesAnUnreadableLeaf(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can open anything")
	}
	dir := t.TempDir()
	secret := filepath.Join(dir, "unreadable")
	testutil.FailErr(t, "write file", os.WriteFile(secret, []byte("x"), 0o000))
	got := fspath.CanonicalPath(secret)
	if got == "" {
		t.Fatalf("CanonicalPath(%q) = empty", secret)
	}
	if want := filepath.Join(fspath.CanonicalPath(dir), "unreadable"); got != want {
		t.Fatalf("CanonicalPath(%q) = %q, want %q", secret, got, want)
	}
}

func TestCanonicalPathLeavesRelativeNamesRelative(t *testing.T) {
	for _, rel := range []string{"relative", "./relative", "a/b", "../up"} {
		got := fspath.CanonicalPath(rel)
		if filepath.IsAbs(got) {
			t.Errorf("CanonicalPath(%q) = %q, want it to stay relative", rel, got)
		}
	}
}

// TestCanonicalPathFollowsASymlinkToASocket covers an unopened socket leaf.
func TestCanonicalPathFollowsASymlinkToASocket(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "a.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Skipf("cannot bind a unix socket here: %v", err)
	}
	defer func() { _ = l.Close() }()

	link := filepath.Join(dir, "link.sock")
	testutil.FailErr(t, "symlink", os.Symlink(sock, link))

	got, want := fspath.CanonicalPath(link), fspath.CanonicalPath(sock)
	if got != want {
		t.Fatalf("CanonicalPath(symlink to socket) = %q, want %q", got, want)
	}
	if filepath.Base(got) != "a.sock" {
		t.Fatalf("CanonicalPath(%q) = %q, want it to name the socket, not the link", link, got)
	}
}

func TestCanonicalPathResolvesDanglingLinkTail(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "missing", "directory")
	link := filepath.Join(dir, "alias")
	testutil.FailErr(t, "create dangling link", os.Symlink(filepath.Join("missing", "directory"), link))
	got := fspath.CanonicalPath(filepath.Join(link, "leaf.txt"))
	want := fspath.CanonicalPath(filepath.Join(target, "leaf.txt"))
	if got != want {
		t.Fatalf("dangling link resolved to %q, want %q", got, want)
	}
	testutil.FailErr(t, "remove alias", os.Remove(link))
	testutil.FailErr(t, "create cyclic link", os.Symlink("alias", link))
	if got := fspath.CanonicalPath(filepath.Join(link, "leaf.txt")); got != "" {
		t.Fatalf("cyclic link retained an identity: %q", got)
	}
}

func TestCanonicalPathPreservesFilenameWhitespace(t *testing.T) {
	dir := t.TempDir()
	const name = "  résumé ' [1].txt  "
	path := filepath.Join(dir, name)
	want := filepath.Join(fspath.CanonicalPath(dir), name)
	if got := fspath.CanonicalPath(path); got != want {
		t.Fatalf("missing path = %q, want %q", got, want)
	}
	testutil.FailErr(t, "create whitespace filename", os.WriteFile(path, []byte("exact name"), 0o600))
	if got := fspath.CanonicalPath(path); got != want {
		t.Fatalf("existing path = %q, want %q", got, want)
	}
	if got := fspath.CanonicalPath(name); got != name {
		t.Fatalf("relative path = %q, want %q", got, name)
	}
}
