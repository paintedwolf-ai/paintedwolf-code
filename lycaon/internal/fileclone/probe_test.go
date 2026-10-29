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
	if _, err := SupportedFrom(source, target); err != nil {
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
	if _, err := SupportedFrom(filepath.Join(t.TempDir(), "missing"), target); err == nil {
		t.Fatal("missing source was reported cloneable")
	}
	entries, err = os.ReadDir(target)
	if err != nil || len(entries) != 0 {
		t.Fatalf("failed source probe leaked files: %v %v", entries, err)
	}
}
