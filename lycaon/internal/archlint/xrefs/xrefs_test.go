package xrefs_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"testing"

	"github.com/lycaon/lycaon/internal/archlint"
	"github.com/lycaon/lycaon/internal/archlint/xrefs"
	"github.com/lycaon/lycaon/internal/testutil"
)

func keys(fs []archlint.Finding) []string {
	var out []string
	for _, f := range fs {
		out = append(out, f.Kind+" "+f.Key)
	}
	sort.Strings(out)
	return out
}

func equal(t *testing.T, got, want []string) {
	t.Helper()
	sort.Strings(want)
	if len(got) != len(want) {
		t.Fatalf("findings = %q, want %q", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("findings = %q, want %q", got, want)
		}
	}
}

func TestRefsReportsCommandsAndScriptsNothingNames(t *testing.T) {
	root := t.TempDir()
	write := func(p, body string, mode os.FileMode) {
		t.Helper()
		full := filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			testutil.FailErr(t, "create directory", err)
		}
		if err := os.WriteFile(full, []byte(body), mode); err != nil {
			testutil.FailErr(t, "write file", err)
		}
	}
	write("lycaon/cmd/lycaon/main.go", "package main\n", 0o644)
	write("lycaon/cmd/lycaon-debug/main.go", "package main\n", 0o644)
	write("lycaon/cmd/orphan/main.go", "package main // cmd/orphan names itself\n", 0o644)
	write("Taskfile.yml", "build: go build ./cmd/lycaon\nrun: bash scripts/used.sh\ncheck: python3 scripts/tool.py\n", 0o644)
	write("scripts/used.sh", "#!/bin/sh\n", 0o755)
	write("scripts/unused.sh", "#!/bin/sh\n# unused.sh runs nothing\n", 0o755)
	write("scripts/test_unused.sh", "#!/bin/sh\nbash unused.sh\n", 0o644)
	write("scripts/tool.py", "#!/usr/bin/env python3\n", 0o755)
	write("scripts/helper.py", "#!/usr/bin/env python3\n", 0o755)
	write("scripts/caller.py", "import helper\n", 0o644)
	write("scripts/library.py", "", 0o644)
	for _, args := range [][]string{{"init", "-q"}, {"add", "-A"}} {
		if out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	got, err := xrefs.Refs(root)
	testutil.FailErr(t, "xrefs.Refs failed", err)
	equal(t, keys(got), []string{
		"unreferenced-command lycaon/cmd/lycaon-debug",
		"unreferenced-command lycaon/cmd/orphan",
		"unreferenced-script scripts/unused.sh",
	})
	if _, err := xrefs.Refs(filepath.Join(root, "missing")); err == nil {
		t.Fatal("a directory outside git must fail")
	}
}
