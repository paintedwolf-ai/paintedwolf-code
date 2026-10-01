package repomap_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/repomap"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestFileTagsMisnamedGoViaParseConfidence(t *testing.T) {
	dir := t.TempDir()
	src := "package main\n\nfunc main() {}\n"
	path := filepath.Join(dir, "notes.qqq")
	testutil.FailErr(t, "write", os.WriteFile(path, []byte(src), 0o644))

	outcome, err := repomap.FileTags(context.Background(), dir, "notes.qqq")
	testutil.FailErr(t, "FileTags", err)
	if outcome.Language != "go" {
		t.Fatalf("language = %q want go", outcome.Language)
	}
	if len(outcome.Tags) == 0 {
		t.Fatalf("tags = %+v want defs", outcome.Tags)
	}
}

func TestFileTagsExtensionlessShebangPython(t *testing.T) {
	dir := t.TempDir()
	src := "#!/usr/bin/env python3\n\ndef run():\n    return 1\n"
	path := filepath.Join(dir, "deploy")
	testutil.FailErr(t, "write", os.WriteFile(path, []byte(src), 0o644))

	outcome, err := repomap.FileTags(context.Background(), dir, "deploy")
	testutil.FailErr(t, "FileTags", err)
	if outcome.Language != "python" {
		t.Fatalf("language = %q want python", outcome.Language)
	}
}
