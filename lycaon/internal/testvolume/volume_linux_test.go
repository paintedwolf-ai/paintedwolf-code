//go:build linux

package testvolume

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestFoldingVolumeTreatsCaseVariantsAsOneName(t *testing.T) {
	volume, ran := New(t, Folding)
	if ran {
		return
	}
	dir := volume.Dir(t, "project")
	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(dir, "Readme.md"), []byte("one\n"), 0o600))
	raw, err := os.ReadFile(filepath.Join(dir, "README.MD"))
	testutil.FailErr(t, "read under another case", err)
	if string(raw) != "one\n" {
		t.Fatalf("contents = %q", raw)
	}
	entries, err := os.ReadDir(dir)
	testutil.FailErr(t, "list", err)
	if len(entries) != 1 || entries[0].Name() != "Readme.md" {
		t.Fatalf("the volume keeps the spelling it was given: %v", entries)
	}
}

func TestDescriptorsUnderNamesWhatHoldsAVolume(t *testing.T) {
	root := t.TempDir()
	if held := descriptorsUnder(root); len(held) != 0 {
		t.Fatalf("an unused directory is held by %v", held)
	}
	f, err := os.Create(filepath.Join(root, "open.txt"))
	testutil.FailErr(t, "create", err)
	defer func() { _ = f.Close() }()
	held := descriptorsUnder(root)
	if !slices.ContainsFunc(held, func(h string) bool { return strings.HasSuffix(h, "="+f.Name()) }) {
		t.Fatalf("the open file is not reported: %v", held)
	}
}
