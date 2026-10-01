package repomap_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/repomap"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestFileTagsPython(t *testing.T) {
	dir := t.TempDir()
	src := "class Game:\n    pass\n\ndef run():\n    return 1\n"
	path := filepath.Join(dir, "main.py")
	testutil.FailErr(t, "write", os.WriteFile(path, []byte(src), 0o644))

	outcome, err := repomap.FileTags(context.Background(), dir, "main.py")
	testutil.FailErr(t, "FileTags", err)
	if len(outcome.Tags) < 2 {
		t.Fatalf("tags = %+v", outcome.Tags)
	}
	if len(outcome.Definitions) != len(outcome.Tags) {
		t.Fatalf("definitions = %d, tags = %d", len(outcome.Definitions), len(outcome.Tags))
	}
	if outcome.Language != "python" {
		t.Fatalf("language = %q want python", outcome.Language)
	}
}

func TestFileTagsPlainTextSkipReason(t *testing.T) {
	dir := t.TempDir()
	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("plain\n"), 0o644))

	outcome, err := repomap.FileTags(context.Background(), dir, "notes.txt")
	testutil.FailErr(t, "FileTags", err)
	if outcome.Skip.NoGrammar != 1 {
		t.Fatalf("skip = %+v want no_grammar=1", outcome.Skip)
	}
	if outcome.Language != "" {
		t.Fatalf("language = %q want none", outcome.Language)
	}
	if len(outcome.Tags) != 0 {
		t.Fatalf("tags = %+v", outcome.Tags)
	}
}
