package sourcecatalog

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/lycaon/lycaon/internal/litprefilter"
	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/testutil"
)

func captureFixture(t *testing.T, content string) (string, Entry, *LiteralSearch) {
	t.Helper()
	root := t.TempDir()
	markLiteralWatch(t, root)
	writeLiteralFixture(t, root, "a.txt", content)
	info, err := os.Stat(filepath.Join(root, "a.txt"))
	testutil.FailErr(t, "stat fixture", err)
	return root, Entry{Path: "a.txt", Size: info.Size(), Mode: uint32(info.Mode()), Modified: info.ModTime()}, New().Literals
}

func TestCapturedContentPublicationFences(t *testing.T) {
	for _, kind := range []string{"path", "root", "closed", "canceled", "size"} {
		t.Run(kind, func(t *testing.T) {
			root, entry, index := captureFixture(t, "old content")
			read := index.BeginRead(root, entry, fileVersion(root, entry))
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
	index.BeginRead(root, entry, fileVersion(root, entry)).Observe(t.Context(), []byte("\ufeffCompleteNeedle"))
	req := litprefilter.AnyOf("\ufeffCompleteNeedle")
	if index.CanPruneBytes(root, req, entry, fileVersion(root, entry)) {
		t.Fatal("byte filter lost the BOM")
	}
	if !index.CanPrune(root, req, entry) {
		t.Fatal("decoded filter retained the BOM")
	}
	if read := index.BeginRead(root, entry, fileVersion(root, entry)); read != nil {
		read.Close()
		t.Fatal("current content was rebuilt")
	}
	index.cache.invalidate(root, []string{entry.Path})
	if index.CanPruneBytes(root, litprefilter.AnyOf("absent-token"), entry, fileVersion(root, entry)) {
		t.Fatal("invalidated filter remained usable")
	}
}

func TestCapturedContentKeepsBinaryAndInvalidUTF8Live(t *testing.T) {
	for _, content := range []string{"\x00binary", "\xffinvalid", "\xff\xfea\x00b\x00c\x00"} {
		root, entry, index := captureFixture(t, content)
		index.BeginRead(root, entry, fileVersion(root, entry)).Observe(t.Context(), []byte(content))
		if index.CanPruneBytes(root, litprefilter.AnyOf("absent-token"), entry, fileVersion(root, entry)) {
			t.Fatalf("non-text bytes were pruned: %q", content)
		}
	}
}

func TestCapturedContentCoalescesConcurrentReaders(t *testing.T) {
	root, entry, index := captureFixture(t, "current content")
	first := index.BeginRead(root, entry, fileVersion(root, entry))
	for range 64 {
		if duplicate := index.BeginRead(root, entry, fileVersion(root, entry)); duplicate != nil {
			duplicate.Close()
			t.Fatal("duplicate content preparation was admitted")
		}
	}
	if len(index.cache.fileReads) != 1 {
		t.Fatalf("tracked %d captures", len(index.cache.fileReads))
	}
	first.Close()
	next := index.BeginRead(root, entry, fileVersion(root, entry))
	if next == nil {
		t.Fatal("abandoned capture prevented retry")
	}
	next.Observe(t.Context(), []byte("current content"))
	if index.cache.files.count != 1 || len(index.cache.fileReads) != 0 {
		t.Fatalf("files=%d reads=%d", index.cache.files.count, len(index.cache.fileReads))
	}
}

func TestCapturedContentInvalidatesSameMetadataWrites(t *testing.T) {
	root, entry, index := captureFixture(t, "old token")
	index.BeginRead(root, entry, fileVersion(root, entry)).Observe(t.Context(), []byte("old token"))
	writeLiteralFixture(t, root, entry.Path, "new token")
	testutil.FailErr(t, "restore metadata", os.Chtimes(filepath.Join(root, entry.Path), entry.Modified, entry.Modified))
	index.cache.invalidate(root, []string{entry.Path})
	if index.CanPruneBytes(root, litprefilter.AnyOf("new token"), entry, fileVersion(root, entry)) {
		t.Fatal("known write reused the old proof")
	}
	index.BeginRead(root, entry, fileVersion(root, entry)).Observe(t.Context(), []byte("new token"))
	if index.CanPruneBytes(root, litprefilter.AnyOf("new token"), entry, fileVersion(root, entry)) || !index.CanPruneBytes(root, litprefilter.AnyOf("old token"), entry, fileVersion(root, entry)) {
		t.Fatal("replacement observation did not describe current bytes")
	}
}

func markLiteralWatch(t *testing.T, root string) {
	t.Helper()
	repochange.MarkCoverageCompleteForTest(root)
	t.Cleanup(func() { repochange.CloseRoot(root) })
}

func TestContentFiltersRequireOneUninterruptedWatchBinding(t *testing.T) {
	root, entry, index := captureFixture(t, "old token")
	index.BeginRead(root, entry, fileVersion(root, entry)).Observe(t.Context(), []byte("old token"))
	repochange.CloseRoot(root)
	writeLiteralFixture(t, root, entry.Path, "new token")
	testutil.FailErr(t, "restore metadata", os.Chtimes(filepath.Join(root, entry.Path), entry.Modified, entry.Modified))
	if index.CanPruneBytes(root, litprefilter.AnyOf("new token"), entry, fileVersion(root, entry)) {
		t.Fatal("unwatched bytes were excluded")
	}
	if read := index.BeginRead(root, entry, fileVersion(root, entry)); read != nil {
		read.Close()
		t.Fatal("unwatched capture retained a proof")
	}
	repochange.MarkCoverageCompleteForTest(root)
	if index.CanPruneBytes(root, litprefilter.AnyOf("new token"), entry, fileVersion(root, entry)) || index.CanPrune(root, litprefilter.AnyOf("new token"), entry) {
		t.Fatal("replacement watch revived an old proof")
	}
	index.BeginRead(root, entry, fileVersion(root, entry)).Observe(t.Context(), []byte("new token"))
	if index.CanPruneBytes(root, litprefilter.AnyOf("new token"), entry, fileVersion(root, entry)) {
		t.Fatal("replacement content was excluded")
	}
}

func TestContentReadCannotPublishAcrossWatchRebinding(t *testing.T) {
	root, entry, index := captureFixture(t, "old token")
	read := index.BeginRead(root, entry, fileVersion(root, entry))
	repochange.CloseRoot(root)
	repochange.MarkCoverageCompleteForTest(root)
	read.Observe(t.Context(), []byte("old token"))
	if index.cache.files.count != 0 {
		t.Fatal("old capture published into a new watch binding")
	}
}

func TestColdUnwatchedContentNeverBecomesAProof(t *testing.T) {
	root, entry, index := captureFixture(t, "current token")
	repochange.CloseRoot(root)
	index.BeginRead(root, entry, fileVersion(root, entry)).Observe(t.Context(), []byte("current token"))
	repochange.MarkCoverageCompleteForTest(root)
	if index.CanPruneBytes(root, litprefilter.AnyOf("absent token"), entry, fileVersion(root, entry)) {
		t.Fatal("pre-binding capture became a proof")
	}
}

func TestCapturedReaderPublishesOnlyCompletePinnedBytes(t *testing.T) {
	for _, kind := range []string{"complete", "partial", "short", "oversize", "error", "canceled"} {
		t.Run(kind, func(t *testing.T) {
			const content = "current content"
			root, entry, index := captureFixture(t, content)
			read := index.BeginRead(root, entry, fileVersion(root, entry))
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var source io.Reader = strings.NewReader(content)
			switch kind {
			case "short":
				source = strings.NewReader(content[:3])
			case "oversize":
				source = strings.NewReader(content + "extra")
			case "error":
				source = io.MultiReader(strings.NewReader(content[:3]), iotest.ErrReader(errors.New("read failed")))
			case "canceled":
				cancel()
			}
			observed := read.Wrap(ctx, source)
			if kind == "partial" {
				observed = io.LimitReader(observed, 3)
			}
			_, _ = io.Copy(io.Discard, observed)
			read.Close()
			want := kind == "complete"
			if read.Learned() != want || (index.cache.files.count == 1) != want || len(index.cache.fileReads) != 0 {
				t.Fatalf("learned=%v files=%d reads=%d", read.Learned(), index.cache.files.count, len(index.cache.fileReads))
			}
		})
	}
}

func TestCatalogUnknownPathInvalidatesInFlightCapturedBytes(t *testing.T) {
	for _, paths := range [][]string{nil, {"a.txt"}} {
		root, entry, index := captureFixture(t, "old token")
		read := index.BeginRead(root, entry, fileVersion(root, entry))
		writeLiteralFixture(t, root, entry.Path, "new token")
		testutil.FailErr(t, "restore metadata", os.Chtimes(filepath.Join(root, entry.Path), entry.Modified, entry.Modified))
		catalog := New()
		catalog.Literals = index
		catalog.InvalidateRoot(root, paths...)
		read.Observe(t.Context(), []byte("old token"))
		if read.Learned() {
			t.Fatal("invalidated capture was published")
		}
		index.BeginRead(root, entry, fileVersion(root, entry)).Observe(t.Context(), []byte("new token"))
		if index.CanPruneBytes(root, litprefilter.AnyOf("new token"), entry, fileVersion(root, entry)) {
			t.Fatal("new bytes were pruned")
		}
	}
}

func TestContentLearningSharesBoundedNonblockingWorkingCapacity(t *testing.T) {
	root, entry, index := captureFixture(t, "small")
	var reads []*ContentRead
	var streams []*contentReadStream
	for i := range literalBuildWorkers {
		observed := entry
		observed.Path = fmt.Sprintf("file-%d", i)
		observed.Size = 1 << 30
		read := index.BeginRead(root, observed, fileVersion(root, entry))
		if read == nil {
			t.Fatalf("reader %d was not admitted", i)
		}
		stream := read.Wrap(t.Context(), strings.NewReader("\ufeffcontent")).(*contentReadStream)
		buf := make([]byte, 3)
		_, _ = stream.Read(buf)
		reads = append(reads, read)
		streams = append(streams, stream)
	}
	next := entry
	next.Path = "extra"
	next.Size = 1 << 30
	if read := index.BeginRead(root, next, fileVersion(root, entry)); read != nil {
		read.Close()
		t.Fatal("working slots exceeded")
	}
	if index.cache.workingBytes > literalLearningByteCap || len(index.cache.fileReads) != literalBuildWorkers {
		t.Fatalf("working=%d reads=%d", index.cache.workingBytes, len(index.cache.fileReads))
	}
	for i, read := range reads {
		read.Close()
		if streams[i].builder != nil {
			t.Fatal("closed reader retained its builder")
		}
	}
	if index.cache.workingBytes != 0 || len(index.cache.fileReads) != 0 {
		t.Fatal("working capacity was not released")
	}
	retry := index.BeginRead(root, next, fileVersion(root, entry))
	if retry == nil {
		t.Fatal("released capacity was not reusable")
	}
	retry.Close()
}

type blockedContentReader struct {
	entered, resume chan struct{}
	content         string
}

func (r blockedContentReader) Read(p []byte) (int, error) {
	close(r.entered)
	<-r.resume
	return copy(p, r.content), io.EOF
}

func TestClosingContentObservationReleasesBlockedReaderCapacity(t *testing.T) {
	root, entry, index := captureFixture(t, "content")
	read := index.BeginRead(root, entry, fileVersion(root, entry))
	source := blockedContentReader{entered: make(chan struct{}), resume: make(chan struct{}), content: "content"}
	observed := read.Wrap(t.Context(), source)
	done := make(chan struct{})
	go func() { _, _ = observed.Read(make([]byte, 32)); close(done) }()
	<-source.entered
	read.Close()
	if index.cache.workingBytes != 0 {
		t.Fatal("blocked source retained learning capacity")
	}
	close(source.resume)
	<-done
	if read.Learned() {
		t.Fatal("closed observer published after source resumed")
	}
}
