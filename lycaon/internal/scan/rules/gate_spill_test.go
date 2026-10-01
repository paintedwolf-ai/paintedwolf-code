package rules

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

// Identical spills skip the write; mismatched destinations are replaced.

func TestSpillFileSkipsAnIdenticalWrite(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "nested", "rule.yaml")
	data := []byte("rules:\n  - id: sample\n")

	testutil.FailErr(t, "first spill", spillFile(dest, data))
	first, err := os.Stat(dest)
	testutil.FailErr(t, "stat after first spill", err)

	testutil.FailErr(t, "second spill", spillFile(dest, data))
	second, err := os.Stat(dest)
	testutil.FailErr(t, "stat after second spill", err)

	if !second.ModTime().Equal(first.ModTime()) {
		t.Fatalf("identical spill rewrote the file: %v then %v", first.ModTime(), second.ModTime())
	}
	got, err := os.ReadFile(dest)
	testutil.FailErr(t, "read spilled rule", err)
	if string(got) != string(data) {
		t.Fatalf("spilled content = %q want %q", got, data)
	}
}

func TestSpillFileReplacesContentThatDoesNotMatch(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "rule.yaml")
	want := []byte("rules:\n  - id: sample\n")
	testutil.FailErr(t, "first spill", spillFile(dest, want))

	// A tampered rule, or a spill truncated by a crash, must be healed.
	testutil.FailErr(t, "tamper", os.WriteFile(dest, []byte("rules: []\n"), 0o600))
	testutil.FailErr(t, "respill", spillFile(dest, want))

	got, err := os.ReadFile(dest)
	testutil.FailErr(t, "read healed rule", err)
	if string(got) != string(want) {
		t.Fatalf("tampered rule was not healed: %q", got)
	}
}

func TestSpillFileCreatesMissingParents(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "a", "b", "c", "rule.yaml")
	testutil.FailErr(t, "spill into a missing tree", spillFile(dest, []byte("rules: []\n")))
	if _, err := os.Stat(dest); err != nil {
		t.Fatalf("spill did not create the path: %v", err)
	}
}
