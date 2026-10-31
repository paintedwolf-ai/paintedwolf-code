package sourcerewind

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/textfile"
)

func TestLinkTransitionRemovesAndRestoresRetainedText(t *testing.T) {
	root := t.TempDir()
	text := []byte("missing.txt")
	linked := sourceledger.RestorableVersion{RootID: "r", Path: "link", State: "link", SHA256: textfile.SHA256(text), Content: text}
	absent := sourceledger.RestorableVersion{RootID: "r", Path: "link", State: "absent"}
	if err := os.Symlink(string(text), filepath.Join(root, "link")); err != nil {
		testutil.FailErr(t, "os.Symlink failed", err)
	}
	file := File{RootPath: root}
	if err := transition(file, linked, absent); err != nil {
		testutil.FailErr(t, "transition failed", err)
	}
	if _, err := os.Lstat(filepath.Join(root, "link")); !os.IsNotExist(err) {
		t.Fatalf("removed link = %v", err)
	}
	if err := transition(file, absent, linked); err != nil {
		testutil.FailErr(t, "transition failed", err)
	}
	if got, err := os.Readlink(filepath.Join(root, "link")); err != nil || got != string(text) {
		t.Fatalf("restored target = %q, %v", got, err)
	}
}

func TestLinkTransitionRefusesTargetOutsideAttachedRoot(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "target")
	text := []byte(outside)
	absent := sourceledger.RestorableVersion{RootID: "r", Path: "link", State: "absent"}
	linked := sourceledger.RestorableVersion{RootID: "r", Path: "link", State: "link", SHA256: textfile.SHA256(text), Content: text}
	if err := transition(File{RootPath: root}, absent, linked); err == nil {
		t.Fatal("rewind accepted an outside link target")
	}
	if _, err := os.Lstat(filepath.Join(root, "link")); !os.IsNotExist(err) {
		t.Fatalf("outside link appeared: %v", err)
	}
}
