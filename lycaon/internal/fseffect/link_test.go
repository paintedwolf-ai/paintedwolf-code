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
