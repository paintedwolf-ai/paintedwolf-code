package survey

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/summarize"
	"github.com/lycaon/lycaon/internal/testutil"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

var implicitTextEncodings = testutil.SelfIdentifyingTextEncodings()

func TestGrepReaderDecodesSelfIdentifyingEncodings(t *testing.T) {
	t.Parallel()
	for _, encoding := range implicitTextEncodings {
		t.Run(encoding, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			path := filepath.Join(dir, "notes.txt")
			data := testutil.EncodeTextFixture(t, "needle\n", encoding)
			if err := os.WriteFile(path, data, 0o644); err != nil {
				t.Fatalf("write fixture: %v", err)
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatalf("stat fixture: %v", err)
			}
			content, binary, truncated, err := readGrepFile(context.Background(), projectpaths.NewReadSession(nativefixture.Boundary(t), nativefixture.Context(dir)), path, info)
			if err != nil || binary || truncated || string(content) != "needle\n" {
				t.Fatalf("readGrepFile = %q binary=%v truncated=%v err=%v", content, binary, truncated, err)
			}
		})
	}
}

func TestDiffReaderDecodesSelfIdentifyingEncodings(t *testing.T) {
	t.Parallel()
	for _, encoding := range implicitTextEncodings {
		t.Run(encoding, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			data := testutil.EncodeTextFixture(t, "line one\n世界\n", encoding)
			if err := os.WriteFile(filepath.Join(dir, "notes.txt"), data, 0o644); err != nil {
				t.Fatalf("write fixture: %v", err)
			}
			ctx := nativefixture.Context(dir)
			content, rel, err := (&DiffTool{Boundary: nativefixture.Boundary(t)}).loadDiffFile(
				context.Background(), ctx, "notes.txt", ctx.ProfileID(),
			)
			if err != nil || rel != "notes.txt" || content != "line one\n世界\n" {
				t.Fatalf("loadDiffFile = content=%q rel=%q err=%v", content, rel, err)
			}
		})
	}
}

func TestWcReaderCountsDecodedTextAndRawBytesForSelfIdentifyingEncodings(t *testing.T) {
	t.Parallel()
	for _, encoding := range implicitTextEncodings {
		t.Run(encoding, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			path := filepath.Join(dir, "notes.txt")
			raw := testutil.EncodeTextFixture(t, "one two\n世界\n", encoding)
			if err := os.WriteFile(path, raw, 0o644); err != nil {
				t.Fatalf("write fixture: %v", err)
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatalf("stat fixture: %v", err)
			}
			entry, err := wcFileEntry(context.Background(), projectpaths.NewReadSession(nativefixture.Boundary(t), nativefixture.Context(dir)), "notes.txt", path, info, true, nil)
			if err != nil {
				t.Fatalf("wcFileEntry %s: %v", encoding, err)
			}
			if entry.Bytes != int64(len(raw)) || entry.Lines == nil || *entry.Lines != 2 || entry.Words == nil || *entry.Words != 3 {
				t.Fatalf("wcFileEntry %s = %+v raw bytes=%d", encoding, entry, len(raw))
			}
		})
	}
}

func TestSummarizeReaderDecodesSelfIdentifyingEncodings(t *testing.T) {
	t.Parallel()
	for _, encoding := range implicitTextEncodings {
		t.Run(encoding, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			path := filepath.Join(dir, "guide.md")
			raw := testutil.EncodeTextFixture(t, "# Guide\n\n世界\n", encoding)
			if err := os.WriteFile(path, raw, 0o644); err != nil {
				t.Fatalf("write fixture: %v", err)
			}
			g := testSummarizeGatherer(t, dir, summarize.DefaultCaps())
			content, err := g.readFileCached(context.Background(), path)
			if err != nil || string(content) != "# Guide\n\n世界\n" {
				t.Fatalf("summarize read %s = %q err=%v", encoding, content, err)
			}
		})
	}
}
