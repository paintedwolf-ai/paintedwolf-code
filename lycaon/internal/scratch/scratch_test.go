package scratch_test

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/scratch"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestEnsureCreatesOnePrivateFolderPerSession(t *testing.T) {
	stateRoot := t.TempDir()
	folders := scratch.New(stateRoot)

	dir, err := folders.Ensure("chat-1")
	testutil.FailErr(t, "ensure chat-1", err)
	want := filepath.Join(fspath.CanonicalPath(stateRoot), "scratch", "chat-1")
	if dir != want {
		t.Fatalf("Ensure = %q, want %q", dir, want)
	}
	info, err := os.Stat(dir)
	testutil.FailErr(t, "stat folder", err)
	if !info.IsDir() || info.Mode().Perm() != 0o700 {
		t.Fatalf("folder mode = %v, want a 0700 directory", info.Mode())
	}

	testutil.FailErr(t, "write probe", os.WriteFile(filepath.Join(dir, "probe.txt"), []byte("kept"), 0o600))
	again, err := folders.Ensure("chat-1")
	testutil.FailErr(t, "ensure again", err)
	if again != dir {
		t.Fatalf("second Ensure = %q, want %q", again, dir)
	}
	if raw, err := os.ReadFile(filepath.Join(dir, "probe.txt")); err != nil || string(raw) != "kept" {
		t.Fatalf("Ensure disturbed existing contents: %q, %v", raw, err)
	}

	worker, err := folders.Ensure("worker-child")
	testutil.FailErr(t, "ensure worker", err)
	if filepath.Dir(worker) != filepath.Dir(dir) {
		t.Fatalf("worker folder %q is not a sibling of %q", worker, dir)
	}
}

func TestEnsureRefusesAPlantedLink(t *testing.T) {
	stateRoot := t.TempDir()
	folders := scratch.New(stateRoot)
	testutil.FailErr(t, "mkdir scratch root", os.MkdirAll(filepath.Join(stateRoot, "scratch"), 0o700))
	target := t.TempDir()
	testutil.FailErr(t, "plant link", os.Symlink(target, filepath.Join(stateRoot, "scratch", "chat-1")))

	if dir, err := folders.Ensure("chat-1"); err == nil {
		t.Fatalf("Ensure followed a planted link to %q", dir)
	}
}

func TestEnsureRefusesALinkedScratchRoot(t *testing.T) {
	stateRoot := t.TempDir()
	folders := scratch.New(stateRoot)
	testutil.FailErr(t, "plant root link", os.Symlink(t.TempDir(), filepath.Join(stateRoot, "scratch")))

	if dir, err := folders.Ensure("chat-1"); err == nil {
		t.Fatalf("Ensure followed a linked scratch root to %q", dir)
	}
}

func TestSessionIDsNameOneFolder(t *testing.T) {
	folders := scratch.New(t.TempDir())
	for _, id := range []string{"", " ", ".", "..", "a/b", `a\b`, "../chat"} {
		if _, err := folders.Ensure(id); !errors.Is(err, scratch.ErrInvalidSessionID) {
			t.Fatalf("Ensure(%q) err = %v, want ErrInvalidSessionID", id, err)
		}
		if err := folders.Remove(id); !errors.Is(err, scratch.ErrInvalidSessionID) {
			t.Fatalf("Remove(%q) err = %v, want ErrInvalidSessionID", id, err)
		}
	}
}

func TestRemoveDeletesOnlyNamedSessions(t *testing.T) {
	folders := scratch.New(t.TempDir())
	coordinator, err := folders.Ensure("chat-1")
	testutil.FailErr(t, "ensure chat-1", err)
	worker, err := folders.Ensure("worker-1")
	testutil.FailErr(t, "ensure worker-1", err)
	other, err := folders.Ensure("chat-2")
	testutil.FailErr(t, "ensure chat-2", err)
	testutil.FailErr(t, "write other", os.WriteFile(filepath.Join(other, "keep.txt"), []byte("x"), 0o600))

	testutil.FailErr(t, "remove tree", folders.Remove("chat-1", "worker-1", "never-created"))
	for _, dir := range []string{coordinator, worker} {
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatalf("%q survived Remove: %v", dir, err)
		}
	}
	if _, err := os.Stat(filepath.Join(other, "keep.txt")); err != nil {
		t.Fatalf("unrelated session scratch was removed: %v", err)
	}
}

func TestReclaimKeepsSessionsInUse(t *testing.T) {
	folders := scratch.New(t.TempDir())
	idle, err := folders.Ensure("idle")
	testutil.FailErr(t, "ensure idle", err)
	busy, err := folders.Ensure("busy")
	testutil.FailErr(t, "ensure busy", err)
	testutil.FailErr(t, "write busy", os.WriteFile(filepath.Join(busy, "live.txt"), []byte("x"), 0o600))

	var released []string
	kept, err := folders.Reclaim(t.Context(), func(id string) (func(), bool) {
		if id == "busy" {
			return nil, false
		}
		return func() { released = append(released, id) }, true
	})
	testutil.FailErr(t, "reclaim", err)
	if !slices.Equal(kept, []string{"busy"}) {
		t.Fatalf("kept = %v, want [busy]", kept)
	}
	if !slices.Equal(released, []string{"idle"}) {
		t.Fatalf("released = %v, want [idle]", released)
	}
	if _, err := os.Stat(idle); !os.IsNotExist(err) {
		t.Fatalf("idle scratch survived Reclaim: %v", err)
	}
	if _, err := os.Stat(filepath.Join(busy, "live.txt")); err != nil {
		t.Fatalf("busy scratch was removed: %v", err)
	}
}

func TestInventoryCountsFilesNotFolders(t *testing.T) {
	folders := scratch.New(t.TempDir())
	present, bytes, err := folders.Inventory(t.Context())
	testutil.FailErr(t, "inventory before any folder", err)
	if present || bytes != 0 {
		t.Fatalf("missing root: present=%v bytes=%d", present, bytes)
	}

	dir, err := folders.Ensure("chat-1")
	testutil.FailErr(t, "ensure", err)
	present, _, err = folders.Inventory(t.Context())
	testutil.FailErr(t, "inventory of empty folder", err)
	if present {
		t.Fatal("an empty folder reported content")
	}

	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(dir, "out.txt"), []byte("hello"), 0o600))
	present, bytes, err = folders.Inventory(t.Context())
	testutil.FailErr(t, "inventory", err)
	if !present || bytes != 5 {
		t.Fatalf("inventory = present %v, %d bytes; want present, 5 bytes", present, bytes)
	}
}
