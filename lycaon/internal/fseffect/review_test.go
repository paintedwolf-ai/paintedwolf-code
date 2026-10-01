package fseffect_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestReviewedStagingCannotChangeDuringApproval(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "AGENTS.md")
	testutil.FailErr(t, "seed destination", os.WriteFile(target, []byte("original"), 0o644))
	var staging string
	_, err := fseffect.Replace(fseffect.ReplaceRequest{
		Location: fseffect.Location{Root: root, Rel: "AGENTS.md"}, Source: strings.NewReader("proposal"), Mode: 0o644,
		ObserveStagingPath: func(path string) { staging = path },
		ReviewStaged: func(fseffect.Target, fseffect.Result) error {
			return os.WriteFile(staging, []byte("unreviewed"), 0o600)
		},
	})
	if err == nil {
		t.Fatal("staging changed during approval but was committed")
	}
	current, err := os.ReadFile(target)
	testutil.FailErr(t, "read destination", err)
	if string(current) != "original" {
		t.Fatalf("unreviewed bytes landed: %q", current)
	}
}
