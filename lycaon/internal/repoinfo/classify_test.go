package repoinfo

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/testutil"
)

// countingSniffer records which files an analysis had to open.
type countingSniffer struct {
	mu    sync.Mutex
	paths []string
}

func (c *countingSniffer) sniff(absPath string) []byte {
	c.mu.Lock()
	c.paths = append(c.paths, absPath)
	c.mu.Unlock()
	return readHead(absPath)
}

func (c *countingSniffer) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.paths)
}

// testCatalog is one engine's catalog over a private config dir. One catalog
// per test keeps a single writer on each index file.
func testCatalog(t *testing.T) *sourcecatalog.Catalog {
	t.Helper()
	t.Setenv("LYCAON_CONFIG_DIR", t.TempDir())
	c := sourcecatalog.New()
	t.Cleanup(func() { testutil.FailErr(t, "drain index", c.Drain(context.Background())) })
	return c
}

// indexOf returns a reader over root's index once no rescan is pending.
func indexOf(t *testing.T, c *sourcecatalog.Catalog, root string) *sourcecatalog.IndexReader {
	t.Helper()
	until := time.Now().Add(30 * time.Second)
	for time.Now().Before(until) {
		reader, status, err := c.Trees.OpenIndex(context.Background(), "p", sourcecatalog.Root{ID: "r", Path: root}, time.Second)
		testutil.FailErr(t, "open index", err)
		if reader != nil {
			if status.Complete && !status.Refreshing {
				t.Cleanup(func() { _ = reader.Close() })
				return reader
			}
			_ = reader.Close()
		}
	}
	t.Fatal("index did not cover the tree")
	return nil
}

// Extensionless files reuse content classification until their metadata changes.
func TestAnalysisReadsOnlyFilesItCannotNameByExtension(t *testing.T) {
	root := t.TempDir()
	// Dominance is by bytes; the Go file outweighs the shell script.
	goSource := "package main\n\nfunc main() {\n\tprintln(\"hello from a source file large enough to dominate\")\n}\n"
	testutil.FailErr(t, "write go", os.WriteFile(filepath.Join(root, "main.go"), []byte(goSource), 0o644))
	blobs := filepath.Join(root, "encoded")
	testutil.FailErr(t, "mkdir encoded fixtures", os.Mkdir(blobs, 0o755))
	for i := range 20 {
		name := filepath.Join(blobs, fmt.Sprintf("%02x-d", i))
		testutil.FailErr(t, "write blob", os.WriteFile(name, []byte{0, 1, 2, 3, byte(i)}, 0o644))
	}
	script := filepath.Join(root, "run")
	testutil.FailErr(t, "write script", os.WriteFile(script, []byte("#!/bin/sh\necho hi\n"), 0o755))

	memo := newLanguageMemo()
	sniffer := &countingSniffer{}
	analysis := catalogAnalysis{projectDir: root, memo: memo, sniff: sniffer.sniff}
	c := testCatalog(t)

	first, err := analysis.run(context.Background(), indexOf(t, c, root))
	testutil.FailErr(t, "first analysis", err)
	if first.FileCount != 22 {
		t.Fatalf("file_count = %d, want 22", first.FileCount)
	}
	if got := sniffer.count(); got != 21 {
		t.Fatalf("first analysis opened %d files, want the 21 without a naming extension", got)
	}
	if len(first.Languages) == 0 || first.Languages[0] != "Go" {
		t.Fatalf("languages = %v, want Go first", first.Languages)
	}

	second, err := analysis.run(context.Background(), indexOf(t, c, root))
	testutil.FailErr(t, "second analysis", err)
	if got := sniffer.count(); got != 21 {
		t.Fatalf("an unchanged tree opened %d more files on re-analysis", got-21)
	}
	if second.FileCount != first.FileCount {
		t.Fatalf("file_count moved from %d to %d without a write", first.FileCount, second.FileCount)
	}
}

// One write re-reads one file. A deleted file leaves the memo so a later file
// at the same path is measured fresh.
func TestAnalysisRereadsOnlyTheChangedFile(t *testing.T) {
	root := t.TempDir()
	blobs := filepath.Join(root, "encoded")
	testutil.FailErr(t, "mkdir encoded fixtures", os.Mkdir(blobs, 0o755))
	for i := range 5 {
		name := filepath.Join(blobs, fmt.Sprintf("%02x-d", i))
		testutil.FailErr(t, "write blob", os.WriteFile(name, []byte{0, 1, byte(i)}, 0o644))
	}
	memo := newLanguageMemo()
	sniffer := &countingSniffer{}
	analysis := catalogAnalysis{projectDir: root, memo: memo, sniff: sniffer.sniff}
	c := testCatalog(t)
	_, err := analysis.run(context.Background(), indexOf(t, c, root))
	testutil.FailErr(t, "first analysis", err)
	if got := sniffer.count(); got != 5 {
		t.Fatalf("first analysis opened %d files, want 5", got)
	}

	changed := filepath.Join(blobs, "03-d")
	past := time.Now().Add(-time.Hour)
	testutil.FailErr(t, "rewrite blob", os.WriteFile(changed, []byte("#!/bin/sh\n"), 0o644))
	testutil.FailErr(t, "age blob", os.Chtimes(changed, past, past))
	testutil.FailErr(t, "remove blob", os.Remove(filepath.Join(blobs, "04-d")))
	c.InvalidateRoot(root)

	_, err = analysis.run(context.Background(), indexOf(t, c, root))
	testutil.FailErr(t, "second analysis", err)
	if got := sniffer.count(); got != 6 {
		t.Fatalf("second analysis opened %d files in total, want 6 (one rewrite)", got)
	}
	memo.mu.Lock()
	_, stale := memo.byRoot[root]["encoded/04-d"]
	memo.mu.Unlock()
	if stale {
		t.Fatal("a deleted file stayed in the memo")
	}
}

// Cancellation stops the sniff fan-out instead of finishing the tree.
func TestAnalysisStopsSniffingWhenCanceled(t *testing.T) {
	root := t.TempDir()
	for i := range 200 {
		name := filepath.Join(root, fmt.Sprintf("%03d-d", i))
		testutil.FailErr(t, "write blob", os.WriteFile(name, []byte{0, byte(i)}, 0o644))
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	analysis := catalogAnalysis{projectDir: root, memo: newLanguageMemo(), sniff: readHead}
	if _, err := analysis.run(ctx, indexOf(t, testCatalog(t), root)); err == nil {
		t.Fatal("a canceled analysis returned a brief")
	}
}
