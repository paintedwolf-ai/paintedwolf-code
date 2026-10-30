package sourcecatalog

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/litprefilter"
	"github.com/lycaon/lycaon/internal/testutil"
)

func captureFixture(t *testing.T, content string) (string, Entry, *LiteralSearch) {
	t.Helper()
	root := t.TempDir()
	writeLiteralFixture(t, root, "a.txt", content)
	info, err := os.Stat(filepath.Join(root, "a.txt"))
	testutil.FailErr(t, "stat fixture", err)
	return root, Entry{Path: "a.txt", Size: info.Size(), Mode: uint32(info.Mode()), Modified: info.ModTime()}, New().Literals
}

func TestCapturedContentPublicationFences(t *testing.T) {
	for _, kind := range []string{"path", "root", "closed", "canceled", "size"} {
		t.Run(kind, func(t *testing.T) {
			root, entry, index := captureFixture(t, "old content")
			read := index.BeginRead(root, entry)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			content := []byte("old content")
			switch kind {
			case "path":
				index.cache.invalidate(root, []string{entry.Path})
			case "root":
				index.cache.invalidate(root, nil)
			case "closed":
				read.Close()
			case "canceled":
				cancel()
			case "size":
				content = content[:len(content)-1]
			}
			read.Observe(ctx, content)
			read.Close()
			if index.cache.files.count != 0 || len(index.cache.fileReads) != 0 {
				t.Fatalf("stale publication or retained capture: files=%d reads=%d", index.cache.files.count, len(index.cache.fileReads))
			}
		})
	}
}

func TestCapturedContentSeparatesByteAndDecodedProjections(t *testing.T) {
	root, entry, index := captureFixture(t, "\ufeffCompleteNeedle")
	index.BeginRead(root, entry).Observe(t.Context(), []byte("\ufeffCompleteNeedle"))
	req := litprefilter.AnyOf("\ufeffCompleteNeedle")
	if index.CanPruneBytes(root, req, entry) {
		t.Fatal("byte filter lost the BOM")
	}
	if !index.CanPrune(root, req, entry) {
		t.Fatal("decoded filter retained the BOM")
	}
	if read := index.BeginRead(root, entry); read != nil {
		read.Close()
		t.Fatal("current content was rebuilt")
	}
	index.cache.invalidate(root, []string{entry.Path})
	if index.CanPruneBytes(root, litprefilter.AnyOf("absent-token"), entry) {
		t.Fatal("invalidated filter remained usable")
	}
}

func TestCapturedContentKeepsBinaryAndInvalidUTF8Live(t *testing.T) {
	for _, content := range []string{"\x00binary", "\xffinvalid", "\xff\xfea\x00b\x00c\x00"} {
		root, entry, index := captureFixture(t, content)
		index.BeginRead(root, entry).Observe(t.Context(), []byte(content))
		if index.CanPruneBytes(root, litprefilter.AnyOf("absent-token"), entry) {
			t.Fatalf("non-text bytes were pruned: %q", content)
		}
	}
}
