package grammars

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Multi-language integration: Go, JavaScript, Python, Rust
// ---------------------------------------------------------------------------

func TestIntegration_MultiLanguage(t *testing.T) {
	dir := t.TempDir()

	files := map[string]string{
		"main.go": "package main\n\nfunc main() {\n\tprintln(\"hello\")\n}\n",
		"app.js":  "function greet(name) {\n  return \"Hello, \" + name;\n}\n",
		"lib.py":  "def add(a, b):\n    return a + b\n",
		"lib.rs":  "fn main() {\n    println!(\"hello\");\n}\n",
	}

	for name, content := range files {
		writeFile(t, filepath.Join(dir, name), content)
	}

	policy := DefaultPolicy()
	policy.MaxConcurrent = 2
	policy.ChannelBuffer = 3

	ch, statsFn := WalkAndParse(context.Background(), dir, policy)

	// Collect results keyed by extension.
	type result struct {
		pf   ParsedFile
		ext  string
		lang string
	}
	var results []result
	for pf := range ch {
		if pf.Err != nil {
			t.Errorf("parse error for %s: %v", pf.Path, pf.Err)
			pf.Close()
			continue
		}
		ext := filepath.Ext(pf.Path)
		langName := ""
		if pf.Lang != nil {
			langName = pf.Lang.Name
		}
		results = append(results, result{pf: pf, ext: ext, lang: langName})
	}

	if len(results) != 4 {
		t.Fatalf("got %d results, want 4", len(results))
	}

	wantLangs := map[string]string{
		".go": "go",
		".js": "javascript",
		".py": "python",
		".rs": "rust",
	}

	seen := map[string]bool{}
	for _, r := range results {
		seen[r.ext] = true

		wantLang, ok := wantLangs[r.ext]
		if !ok {
			t.Errorf("unexpected extension: %s", r.ext)
			r.pf.Close()
			continue
		}
		if r.lang != wantLang {
			t.Errorf("file %s: Lang.Name = %q, want %q", r.pf.Path, r.lang, wantLang)
		}

		// Verify tree is valid.
		if r.pf.Tree == nil {
			t.Errorf("file %s: Tree is nil", r.pf.Path)
		} else {
			root := r.pf.Tree.RootNode()
			if root == nil {
				t.Errorf("file %s: RootNode is nil", r.pf.Path)
			}
		}

		if len(r.pf.Source) == 0 {
			t.Errorf("file %s: Source is empty", r.pf.Path)
		}

		r.pf.Close()
	}

	for ext := range wantLangs {
		if !seen[ext] {
			t.Errorf("missing results for extension %s", ext)
		}
	}

	stats := statsFn()
	if stats.FilesFound != 4 {
		t.Errorf("FilesFound = %d, want 4", stats.FilesFound)
	}
	if stats.FilesParsed != 4 {
		t.Errorf("FilesParsed = %d, want 4", stats.FilesParsed)
	}
	if stats.FilesFailed != 0 {
		t.Errorf("FilesFailed = %d, want 0", stats.FilesFailed)
	}
	if stats.BytesParsed == 0 {
		t.Error("BytesParsed should be > 0")
	}
}

// ---------------------------------------------------------------------------
// Backpressure: slow consumer with constrained concurrency
// ---------------------------------------------------------------------------

func TestIntegration_Backpressure(t *testing.T) {
	const totalFiles = 50
	dir := t.TempDir()

	for i := 0; i < totalFiles; i++ {
		writeFile(t, filepath.Join(dir, fmt.Sprintf("f%03d.go", i)),
			fmt.Sprintf("package main\n\nfunc f%d() {}\n", i))
	}

	policy := DefaultPolicy()
	policy.MaxConcurrent = 2
	policy.ChannelBuffer = 3

	ch, statsFn := WalkAndParse(context.Background(), dir, policy)

	// Slow consumer: sleep 5ms between each read to create backpressure.
	var received int
	for pf := range ch {
		if pf.Err != nil {
			t.Errorf("error on %s: %v", pf.Path, pf.Err)
		}
		pf.Close()
		received++
		time.Sleep(5 * time.Millisecond)
	}

	if received != totalFiles {
		t.Errorf("received %d files, want %d", received, totalFiles)
	}

	stats := statsFn()
	if stats.FilesParsed != totalFiles {
		t.Errorf("FilesParsed = %d, want %d", stats.FilesParsed, totalFiles)
	}
	if stats.FilesFailed != 0 {
		t.Errorf("FilesFailed = %d, want 0", stats.FilesFailed)
	}
	if stats.FilesFound != totalFiles {
		t.Errorf("FilesFound = %d, want %d", stats.FilesFound, totalFiles)
	}
}

// ---------------------------------------------------------------------------
// Close lifecycle: tree usable before Close, nil after, double Close safe
// ---------------------------------------------------------------------------

func TestIntegration_CloseLifecycle(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "main.go"), "package main\n\nfunc main() {}\n")

	policy := DefaultPolicy()
	policy.MaxConcurrent = 1
	policy.ChannelBuffer = 2

	ch, statsFn := WalkAndParse(context.Background(), dir, policy)

	for pf := range ch {
		if pf.Err != nil {
			t.Fatalf("unexpected error: %v", pf.Err)
		}

		// Tree must be usable before Close.
		if pf.Tree == nil {
			t.Fatal("Tree is nil before Close")
		}
		root := pf.Tree.RootNode()
		if root == nil {
			t.Fatal("RootNode is nil before Close")
		}
		nodeType := pf.Tree.NodeType(root)
		if nodeType != "source_file" {
			t.Errorf("root node type = %q, want source_file", nodeType)
		}

		// First Close.
		pf.Close()

		// After Close, Tree must be nil.
		if pf.Tree != nil {
			t.Error("Tree should be nil after Close")
		}
		if pf.Source != nil {
			t.Error("Source should be nil after Close")
		}

		// Double Close must not panic.
		pf.Close()

		// Still nil after double Close.
		if pf.Tree != nil {
			t.Error("Tree should be nil after double Close")
		}
	}

	_ = statsFn()
}

// ---------------------------------------------------------------------------
// Empty directory: 0 results, stats all zero
// ---------------------------------------------------------------------------

func TestIntegration_EmptyDirectory(t *testing.T) {
	dir := t.TempDir()

	policy := DefaultPolicy()
	policy.MaxConcurrent = 2
	policy.ChannelBuffer = 3

	ch, statsFn := WalkAndParse(context.Background(), dir, policy)

	count := 0
	for range ch {
		count++
	}

	if count != 0 {
		t.Errorf("got %d results from empty dir, want 0", count)
	}

	stats := statsFn()
	if stats.FilesFound != 0 {
		t.Errorf("FilesFound = %d, want 0", stats.FilesFound)
	}
	if stats.FilesParsed != 0 {
		t.Errorf("FilesParsed = %d, want 0", stats.FilesParsed)
	}
	if stats.FilesFailed != 0 {
		t.Errorf("FilesFailed = %d, want 0", stats.FilesFailed)
	}
	if stats.LargeFiles != 0 {
		t.Errorf("LargeFiles = %d, want 0", stats.LargeFiles)
	}
	if stats.BinarySkipped != 0 {
		t.Errorf("BinarySkipped = %d, want 0", stats.BinarySkipped)
	}
	if stats.BytesParsed != 0 {
		t.Errorf("BytesParsed = %d, want 0", stats.BytesParsed)
	}
}

// ---------------------------------------------------------------------------
// Large repo simulation: 100+ small files + 2 large files
// ---------------------------------------------------------------------------

func TestIntegration_LargeRepoSimulation(t *testing.T) {
	const (
		smallCount     = 100
		largeCount     = 2
		largeThreshold = 200 // bytes
	)

	dir := t.TempDir()

	// Create subdirectories to simulate realistic structure.
	for _, sub := range []string{"cmd", "pkg", "internal"} {
		os.MkdirAll(filepath.Join(dir, sub), 0o755)
	}

	// 100 small Go files spread across subdirectories.
	subs := []string{"cmd", "pkg", "internal"}
	for i := 0; i < smallCount; i++ {
		sub := subs[i%len(subs)]
		writeFile(t, filepath.Join(dir, sub, fmt.Sprintf("f%03d.go", i)),
			fmt.Sprintf("package %s\n\nfunc F%d() int { return %d }\n", sub, i, i))
	}

	// 2 large files that exceed the threshold.
	padding := strings.Repeat("// padding\n", 30)
	for i := 0; i < largeCount; i++ {
		content := fmt.Sprintf("package main\n\nfunc Large%d() {\n%s}\n", i, padding)
		if int64(len(content)) < int64(largeThreshold) {
			t.Fatalf("large file %d is only %d bytes, need >= %d", i, len(content), largeThreshold)
		}
		writeFile(t, filepath.Join(dir, fmt.Sprintf("large%d.go", i)), content)
	}

	policy := DefaultPolicy()
	policy.LargeFileThreshold = largeThreshold
	policy.MaxConcurrent = 4
	policy.ChannelBuffer = 5

	var mu sync.Mutex
	var largeFileEvents []string
	var progressPhases []string
	policy.OnProgress = func(ev ProgressEvent) {
		mu.Lock()
		defer mu.Unlock()
		progressPhases = append(progressPhases, ev.Phase)
		if ev.Phase == "large_file" {
			largeFileEvents = append(largeFileEvents, ev.Path)
		}
	}

	ch, statsFn := WalkAndParse(context.Background(), dir, policy)

	var parsed int
	var errors int
	for pf := range ch {
		if pf.Err != nil {
			errors++
		} else {
			parsed++
		}
		pf.Close()
	}

	stats := statsFn()

	// All files should parse successfully.
	totalExpected := smallCount + largeCount
	if parsed != totalExpected {
		t.Errorf("parsed = %d, want %d (errors=%d)", parsed, totalExpected, errors)
	}
	if stats.FilesParsed != totalExpected {
		t.Errorf("stats.FilesParsed = %d, want %d", stats.FilesParsed, totalExpected)
	}
	if stats.FilesFailed != 0 {
		t.Errorf("stats.FilesFailed = %d, want 0", stats.FilesFailed)
	}

	// Large files should be counted.
	if stats.LargeFiles != largeCount {
		t.Errorf("stats.LargeFiles = %d, want %d", stats.LargeFiles, largeCount)
	}

	// Large file progress events should fire.
	mu.Lock()
	lfEvents := len(largeFileEvents)
	mu.Unlock()
	if lfEvents != largeCount {
		t.Errorf("large_file progress events = %d, want %d", lfEvents, largeCount)
	}

	// Stats should have positive bytes parsed.
	if stats.BytesParsed == 0 {
		t.Error("BytesParsed should be > 0")
	}

	// Verify required progress phases appeared.
	mu.Lock()
	phaseSet := map[string]bool{}
	for _, p := range progressPhases {
		phaseSet[p] = true
	}
	mu.Unlock()

	for _, required := range []string{"walking", "parsing", "large_file", "walk_complete", "done"} {
		if !phaseSet[required] {
			t.Errorf("missing progress phase: %s", required)
		}
	}
}
