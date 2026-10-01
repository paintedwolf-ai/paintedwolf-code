package gtsqueries_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/fileoutline"
	"github.com/lycaon/lycaon/internal/repomap"
	_ "github.com/lycaon/lycaon/internal/repomap/gtsqueries"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestMarkdownOutlineUsesMarkdownParser(t *testing.T) {
	dir := t.TempDir()
	src := strings.Join([]string{
		"# Title",
		"",
		"## Section",
		"",
		"body",
	}, "\n")
	path := filepath.Join(dir, "doc.md")
	testutil.FailErr(t, "write", os.WriteFile(path, []byte(src), 0o644))

	out, err := fileoutline.Build(context.Background(), dir, "doc.md")
	testutil.FailErr(t, "Build", err)
	if out.Source != "markdown" {
		t.Fatalf("source = %q want markdown", out.Source)
	}
	if len(out.Symbols) < 2 {
		t.Fatalf("symbols = %+v", out.Symbols)
	}
}

func TestFileTagsMarkdownHeadings(t *testing.T) {
	dir := t.TempDir()
	src := "# Alpha\n\n## Beta\n"
	path := filepath.Join(dir, "readme.md")
	testutil.FailErr(t, "write", os.WriteFile(path, []byte(src), 0o644))

	outcome, err := repomap.FileTags(context.Background(), dir, "readme.md")
	testutil.FailErr(t, "FileTags", err)
	if len(outcome.Tags) < 2 {
		t.Fatalf("tags = %+v", outcome.Tags)
	}
}
