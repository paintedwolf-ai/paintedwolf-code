package main

import (
	"bytes"
	"path/filepath"
	"testing"
)

func TestTreeCommandWritesTheFixtureTree(t *testing.T) {
	var stdout bytes.Buffer
	if code := treeMain([]string{"--root", filepath.Join(t.TempDir(), "tree"), "--files", "7"}, &stdout); code != 0 {
		t.Fatalf("tree exited %d", code)
	}
	if !bytes.Contains(stdout.Bytes(), []byte("7 files")) {
		t.Fatalf("tree summary = %q", stdout.String())
	}
	if code := treeMain(nil, &stdout); code != 2 {
		t.Fatalf("tree without a root exited %d", code)
	}
	if code := treeMain([]string{"--root", t.TempDir(), "--files", "0"}, &stdout); code != 1 {
		t.Fatalf("tree of no files exited %d", code)
	}
	if code := treeMain([]string{"--unknown"}, &stdout); code != 2 {
		t.Fatalf("tree with an unknown flag exited %d", code)
	}
}

func TestSidecarCommandRefusesIncompleteRequests(t *testing.T) {
	var stdout bytes.Buffer
	missing := filepath.Join(t.TempDir(), "missing-engine")
	for _, args := range [][]string{
		{},
		{"--binary", missing, "--installed", "--soak", "1m"},
		{"--binary", missing, "--iterations", "0"},
		{"--binary", missing, "--soak", "-1s"},
		{"--unknown"},
	} {
		if code := sidecarMain(args, &stdout); code != 2 {
			t.Fatalf("sidecar %v exited %d, want 2", args, code)
		}
	}
	if code := sidecarMain([]string{"--binary", missing, "--output", filepath.Join(t.TempDir(), "r.json")}, &stdout); code != 1 {
		t.Fatalf("sidecar with a missing engine exited %d, want 1", code)
	}
}
