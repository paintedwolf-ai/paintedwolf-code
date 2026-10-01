package fseffect

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestRenameNoReplacePreservesConcurrentOccupant(t *testing.T) {
	root := t.TempDir()
	testutil.FailErr(t, "seed source", os.WriteFile(filepath.Join(root, "source"), []byte("saved"), 0o600))
	testutil.FailErr(t, "seed occupant", os.WriteFile(filepath.Join(root, "dest"), []byte("new"), 0o600))
	if err := RenameNoReplace(root, "source", "dest"); !os.IsExist(err) {
		t.Fatalf("rename occupied destination: %v", err)
	}
	for name, want := range map[string]string{"source": "saved", "dest": "new"} {
		content, err := os.ReadFile(filepath.Join(root, name))
		testutil.FailErr(t, "read "+name, err)
		if string(content) != want {
			t.Fatalf("%s = %q", name, content)
		}
	}
}

func TestRenameNoReplacePublishesDirectoryAndRefusesLinkedParent(t *testing.T) {
	root := t.TempDir()
	testutil.FailErr(t, "seed folder", os.Mkdir(filepath.Join(root, "source"), 0o700))
	testutil.FailErr(t, "publish folder", RenameNoReplace(root, "source", "dest"))
	testutil.FailErr(t, "seed link", os.Symlink(t.TempDir(), filepath.Join(root, "outside")))
	if err := RenameNoReplace(root, "dest", "outside/moved"); err == nil {
		t.Fatal("published through linked parent")
	}
	if _, err := os.Stat(filepath.Join(root, "dest")); err != nil {
		t.Fatalf("source lost: %v", err)
	}
}

func TestRenameNoReplacePreservesLiteralWhitespace(t *testing.T) {
	root := t.TempDir()
	testutil.FailErr(t, "seed selected name", os.WriteFile(filepath.Join(root, " source "), []byte("selected"), 0o600))
	testutil.FailErr(t, "seed similar name", os.WriteFile(filepath.Join(root, "target"), []byte("other"), 0o600))
	testutil.FailErr(t, "rename literal path", RenameNoReplace(root, " source ", " target "))
	for _, tc := range []struct{ name, want string }{{" target ", "selected"}, {"target", "other"}} {
		name, want := tc.name, tc.want
		body, err := os.ReadFile(filepath.Join(root, name))
		testutil.FailErr(t, "read literal path", err)
		if string(body) != want {
			t.Fatalf("%q = %q, want %q", name, body, want)
		}
	}
}
