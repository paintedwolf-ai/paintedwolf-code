package fileclone

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestSourceCloneProbeLeavesSourceAndTargetUntouched(t *testing.T) {
	source := filepath.Join(t.TempDir(), "source")
	body := bytes.Repeat([]byte("immutable source\n"), 1024)
	if err := os.WriteFile(source, body, 0400); err != nil {
		t.Fatal(err)
	}
	target := t.TempDir()
	if _, err := SupportedFrom(t.Context(), source, target); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(body, after) {
		t.Fatal("clone capability probe changed source")
	}
	entries, err := os.ReadDir(target)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("capability probe leaked files: %v", entries)
	}
	if _, err := SupportedFrom(t.Context(), filepath.Join(t.TempDir(), "missing"), target); err == nil {
		t.Fatal("missing source was reported cloneable")
	}
	entries, err = os.ReadDir(target)
	if err != nil || len(entries) != 0 {
		t.Fatalf("failed source probe leaked files: %v %v", entries, err)
	}
}

func TestVolumeCloneProbeOwnsItsTemporaryFiles(t *testing.T) {
	root := t.TempDir()
	if _, err := SupportedAt(root); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("volume probe leaked files: %v %v", entries, err)
	}
	if _, err := SupportedAt(filepath.Join(root, "missing")); err == nil {
		t.Fatal("missing probe volume was accepted")
	}
}
