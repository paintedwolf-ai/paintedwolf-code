package grammars

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/odvcencio/gotreesitter"
)

// ParsePolicy controls how WalkAndParse discovers and parses files.
// Configure concurrency limits, file size thresholds, directory/extension
// filters, and optional hooks for progress reporting and file filtering.
// Use [DefaultPolicy] to get a policy with sensible defaults.
type ParsePolicy struct {
	// LargeFileThreshold is the byte size above which a file is parsed with
	// exclusive access to all worker slots (serialized). Files at or above
	// this threshold are considered "large."
	LargeFileThreshold int64

	// MaxConcurrent limits the number of files parsed simultaneously.
	MaxConcurrent int

	// ChannelBuffer is the buffer size for the output channel. Must be at
	// least MaxConcurrent+1 to prevent deadlock when workers hold semaphore
	// slots while sending on the channel.
	ChannelBuffer int

	// SkipDirs lists directory base names to skip entirely during the walk.
	SkipDirs []string

	// ShouldSkipDir, if non-nil, is called for each directory before descent.
	// The root directory is never skipped. Return true to skip the directory
	// and all descendants.
	ShouldSkipDir func(path string) bool

	// SkipExtensions lists file suffixes (e.g., ".min.js") to skip.
	SkipExtensions []string

	// ShouldParse, if non-nil, is called for each candidate file after
	// language detection. Return false to skip the file.
	ShouldParse func(path string, size int64, modTime time.Time) bool

	// SkipTreeParse, if non-nil, is called for candidate files that pass
	// ShouldParse. Return true to read the file (populating Source) but
	// skip the tree-sitter parse (Tree will be nil, Err will be nil).
	// This is useful for large generated files where the consumer wants
	// the raw source bytes for lightweight extraction without paying for
	// a full AST parse.
	SkipTreeParse func(path string, size int64) bool

	// OnProgress, if non-nil, receives progress events during the walk.
	OnProgress func(ProgressEvent)
}

// ProgressEvent reports progress during [WalkAndParse]. The Phase field
// indicates the stage of processing:
//
//   - "walking"       — a file was discovered and queued for parsing
//   - "parsing"       — a file is being parsed (normal or large)
//   - "large_file"    — a large file is acquiring exclusive access
//   - "walk_complete" — directory walk finished; Total is set
//   - "done"          — all parsing complete, channel about to close
type ProgressEvent struct {
	Phase   string // one of: "walking", "parsing", "large_file", "walk_complete", "done"
	Path    string // file path (empty for walk_complete and done phases)
	Size    int64  // file size in bytes
	FileNum int    // 1-based ordinal of this file among discovered files
	Total   int    // total files found (set only in walk_complete phase)
	Message string // optional human-readable detail
}

// ParsedFile is a single result from [WalkAndParse]. Each ParsedFile owns its
// BoundTree and Source slice. The consumer MUST call [ParsedFile.Close] when
// finished inspecting the tree to release arena memory. Failing to close
// results will leak memory proportional to the parsed file sizes.
//
// When Err is non-nil, check IsRead to distinguish I/O errors (IsRead=false,
// file could not be read) from parse errors (IsRead=true, file was read but
// the grammar could not parse it). On I/O errors, Source and Tree are nil.
type ParsedFile struct {
	Path   string                  // absolute path to the source file
	Tree   *gotreesitter.BoundTree // parsed AST; nil on error. Consumer MUST call Close()
	Lang   *LangEntry              // detected language entry
	Source []byte                  // raw file contents; nil on I/O error
	Size   int64                   // file size in bytes (from stat, before read)
	Err    error                   // nil on success
	IsRead bool                    // false = I/O error during read, true = file was read (parse may have failed)
}

// Close releases the BoundTree's arena memory and nils both Tree and Source
// to allow garbage collection. Close is safe to call multiple times and on a
// nil receiver.
//
// Ownership rule: the consumer that receives a ParsedFile from the channel
// owns it. Once Close is called, Tree becomes nil and must not be used.
// Always defer Close or call it explicitly after processing each result:
//
//	for pf := range ch {
//	    if pf.Err != nil { handleErr(pf); pf.Close(); continue }
//	    process(pf.Tree, pf.Source)
//	    pf.Close()
//	}
func (pf *ParsedFile) Close() {
	if pf == nil {
		return
	}
	if pf.Tree != nil {
		pf.Tree.Release()
		pf.Tree = nil
	}
	pf.Source = nil
}

// WalkStats summarizes the results of a [WalkAndParse] run. The stats function
// returned by WalkAndParse blocks until all work is done, then returns this
// snapshot.
type WalkStats struct {
	FilesFound    int   // files with a recognized language extension
	FilesParsed   int   // files successfully parsed into a BoundTree
	FilesFailed   int   // files that encountered read or parse errors
	FilesFiltered int   // files skipped by SkipExtensions or ShouldParse
	LargeFiles    int   // files at or above LargeFileThreshold
	BinarySkipped int   // files detected as binary (NUL in first 8 KB)
	BytesParsed   int64 // total bytes of successfully parsed files
}

// DefaultPolicy returns a [ParsePolicy] with sensible defaults for typical
// source repositories:
//
//   - LargeFileThreshold: 256 KB (env: GTS_LARGE_FILE_THRESHOLD, in bytes)
//   - MaxConcurrent: runtime.GOMAXPROCS(0) (env: GTS_MAX_CONCURRENT)
//   - ChannelBuffer: MaxConcurrent + 1
//   - SkipDirs: .git, .graft, .hg, .svn, vendor, node_modules
//   - SkipExtensions: .min.js, .min.css, .map, .wasm
//
// Environment variables are read on each call and must be positive integers.
// Invalid or non-positive values are silently ignored (defaults apply).
func DefaultPolicy() ParsePolicy {
	threshold := int64(256 * 1024)
	if v := os.Getenv("GTS_LARGE_FILE_THRESHOLD"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			threshold = n
		}
	}

	maxConc := runtime.GOMAXPROCS(0)
	if v := os.Getenv("GTS_MAX_CONCURRENT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			maxConc = n
		}
	}

	return ParsePolicy{
		LargeFileThreshold: threshold,
		MaxConcurrent:      maxConc,
		ChannelBuffer:      maxConc + 1,
		SkipDirs:           []string{".git", ".graft", ".hg", ".svn", "vendor", "node_modules"},
		SkipExtensions:     []string{".min.js", ".min.css", ".map", ".wasm"},
	}
}

// WalkAndParse walks root, discovers source files, and streams parsed results
// on the returned channel. The caller must drain the channel to completion.
// The returned function provides aggregate statistics and blocks until the
// walk is fully complete.
//
// Pipeline:
//  1. Walk with filepath.WalkDir, skipping SkipDirs, ShouldSkipDir, and SkipExtensions.
//  2. Detect language via [DetectLanguage]; skip unknown files.
//  3. Skip binary files (NUL byte in first 8 KB).
//  4. Call ShouldParse hook if set; skip if it returns false.
//  5. Normal files (< LargeFileThreshold): acquire 1 semaphore slot, read+parse
//     in a goroutine, send result, then release.
//  6. Large files (>= LargeFileThreshold): acquire ALL slots, parse inline,
//     send result, release ALL slots.
//
// Backpressure: workers release the semaphore AFTER sending on the channel,
// not before. ChannelBuffer must be at least MaxConcurrent+1 to prevent
// deadlock. A slow consumer naturally throttles the producer because workers
// block on the channel send while holding their semaphore slot.
//
// Cancellation: pass a cancellable context to stop the walk early. In-flight
// parses may complete, but no new files will be dispatched. The channel will
// close promptly after cancellation.
//
// Usage:
//
//	policy := grammars.DefaultPolicy()
//	ch, statsFn := grammars.WalkAndParse(ctx, "/path/to/repo", policy)
//
//	for pf := range ch {
//	    if pf.Err != nil {
//	        log.Printf("error: %s: %v", pf.Path, pf.Err)
//	        pf.Close()
//	        continue
//	    }
//	    root := pf.Tree.RootNode()
//	    // ... inspect the AST ...
//	    pf.Close() // MUST call to release tree memory
//	}
//
//	stats := statsFn() // blocks until fully done; safe to call after draining
//	fmt.Printf("parsed %d files (%d bytes)\n", stats.FilesParsed, stats.BytesParsed)
func WalkAndParse(ctx context.Context, root string, policy ParsePolicy) (<-chan ParsedFile, func() WalkStats) {
	ch := make(chan ParsedFile, policy.ChannelBuffer)

	var stats WalkStats
	var mu sync.Mutex
	var bytesTotal int64

	sem := make(chan struct{}, policy.MaxConcurrent)
	var wg sync.WaitGroup

	skipDirSet := make(map[string]struct{}, len(policy.SkipDirs))
	for _, d := range policy.SkipDirs {
		skipDirSet[d] = struct{}{}
	}

	var filesFound int32

	progress := func(ev ProgressEvent) {
		if policy.OnProgress != nil {
			policy.OnProgress(ev)
		}
	}

	done := make(chan struct{})

	go func() {
		defer close(done)
		defer close(ch)

		_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err != nil {
				return nil // skip inaccessible entries
			}

			if d.IsDir() {
				base := filepath.Base(p)
				if _, skip := skipDirSet[base]; skip && p != root {
					return filepath.SkipDir
				}
				if policy.ShouldSkipDir != nil && p != root && policy.ShouldSkipDir(p) {
					return filepath.SkipDir
				}
				return nil
			}

			// Skip non-regular files (symlinks, devices, etc.)
			if !d.Type().IsRegular() {
				return nil
			}

			// Check skip extensions.
			name := d.Name()
			for _, ext := range policy.SkipExtensions {
				if strings.HasSuffix(name, ext) {
					mu.Lock()
					stats.FilesFiltered++
					mu.Unlock()
					return nil
				}
			}

			// Detect language.
			lang := DetectLanguage(name)
			if lang == nil {
				return nil
			}

			num := int(atomic.AddInt32(&filesFound, 1))

			// Get file info for ShouldParse.
			info, infoErr := d.Info()
			if infoErr != nil {
				return nil
			}
			fileSize := info.Size()

			mu.Lock()
			stats.FilesFound++
			mu.Unlock()

			// ShouldParse hook.
			if policy.ShouldParse != nil {
				if !policy.ShouldParse(p, fileSize, info.ModTime()) {
					mu.Lock()
					stats.FilesFiltered++
					mu.Unlock()
					return nil
				}
			}

			// Binary file detection: check first 8 KB for NUL bytes.
			if bin, _ := checkBinaryFile(p); bin {
				mu.Lock()
				stats.BinarySkipped++
				mu.Unlock()
				return nil
			}

			progress(ProgressEvent{
				Phase:   "walking",
				Path:    p,
				Size:    fileSize,
				FileNum: num,
			})

			skipTree := policy.SkipTreeParse != nil && policy.SkipTreeParse(p, fileSize)
			isLarge := fileSize >= policy.LargeFileThreshold

			if isLarge {
				mu.Lock()
				stats.LargeFiles++
				mu.Unlock()

				progress(ProgressEvent{
					Phase:   "large_file",
					Path:    p,
					Size:    fileSize,
					FileNum: num,
					Message: "acquiring exclusive access",
				})

				// Acquire ALL semaphore slots.
				for i := 0; i < policy.MaxConcurrent; i++ {
					sem <- struct{}{}
				}

				// Wait for in-flight workers to finish before parsing inline.
				wg.Wait()

				var pf ParsedFile
				if skipTree {
					pf = readOnly(p, lang, fileSize)
				} else {
					pf = parseOne(p, lang, fileSize)
				}
				if pf.Err != nil {
					mu.Lock()
					stats.FilesFailed++
					mu.Unlock()
				} else {
					mu.Lock()
					stats.FilesParsed++
					mu.Unlock()
					atomic.AddInt64(&bytesTotal, fileSize)
				}

				progress(ProgressEvent{
					Phase:   "parsing",
					Path:    p,
					Size:    fileSize,
					FileNum: num,
				})

				ch <- pf

				// Release ALL slots.
				for i := 0; i < policy.MaxConcurrent; i++ {
					<-sem
				}
			} else {
				// Normal file: acquire 1 slot.
				sem <- struct{}{}
				wg.Add(1)

				go func(filePath string, entry *LangEntry, size int64, fileNum int, readOnlyMode bool) {
					defer wg.Done()

					// Check for cancellation before doing work.
					if ctx.Err() != nil {
						<-sem
						return
					}

					progress(ProgressEvent{
						Phase:   "parsing",
						Path:    filePath,
						Size:    size,
						FileNum: fileNum,
					})

					var pf ParsedFile
					if readOnlyMode {
						pf = readOnly(filePath, entry, size)
					} else {
						pf = parseOne(filePath, entry, size)
					}
					if pf.Err != nil {
						mu.Lock()
						stats.FilesFailed++
						mu.Unlock()
					} else {
						mu.Lock()
						stats.FilesParsed++
						mu.Unlock()
						atomic.AddInt64(&bytesTotal, size)
					}

					// Send BEFORE releasing semaphore (critical for backpressure).
					ch <- pf
					<-sem
				}(p, lang, fileSize, num, skipTree)
			}

			return nil
		})

		// Walk complete — wait for all in-flight goroutines.
		progress(ProgressEvent{
			Phase: "walk_complete",
			Total: int(atomic.LoadInt32(&filesFound)),
		})
		wg.Wait()

		mu.Lock()
		stats.BytesParsed = atomic.LoadInt64(&bytesTotal)
		mu.Unlock()

		progress(ProgressEvent{Phase: "done"})
	}()

	statsFn := func() WalkStats {
		<-done
		mu.Lock()
		defer mu.Unlock()
		return stats
	}

	return ch, statsFn
}

// binaryCheckSize is the number of bytes inspected for NUL-byte detection.
const binaryCheckSize = 8192

// isBinary returns true if the first 8 KB of data contain a NUL byte,
// indicating the file is likely binary.
func isBinary(data []byte) bool {
	end := len(data)
	if end > binaryCheckSize {
		end = binaryCheckSize
	}
	for i := 0; i < end; i++ {
		if data[i] == 0 {
			return true
		}
	}
	return false
}

// checkBinaryFile reads the first 8 KB of the file at path and returns true
// if it appears to be a binary file (contains a NUL byte). Returns false and
// a non-nil error if the file cannot be opened/read.
func checkBinaryFile(path string) (binary bool, err error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()

	buf := make([]byte, binaryCheckSize)
	n, err := io.ReadAtLeast(f, buf, 1)
	if err != nil && err != io.ErrUnexpectedEOF {
		// Empty file or genuine read error — not binary.
		return false, nil
	}
	return isBinary(buf[:n]), nil
}

// readOnly reads a file without parsing it, returning a ParsedFile with
// Source populated but Tree nil. Used when SkipTreeParse returns true.
func readOnly(path string, lang *LangEntry, size int64) ParsedFile {
	src, err := os.ReadFile(path)
	if err != nil {
		return ParsedFile{
			Path:   path,
			Lang:   lang,
			Size:   size,
			Err:    err,
			IsRead: false,
		}
	}
	return ParsedFile{
		Path:   path,
		Lang:   lang,
		Source: src,
		Size:   size,
		IsRead: true,
	}
}

// parseOne reads and parses a single file, returning a ParsedFile.
func parseOne(path string, lang *LangEntry, size int64) ParsedFile {
	src, err := os.ReadFile(path)
	if err != nil {
		return ParsedFile{
			Path:   path,
			Lang:   lang,
			Size:   size,
			Err:    err,
			IsRead: false,
		}
	}

	tree, err := ParseFilePooled(filepath.Base(path), src)
	if err != nil {
		return ParsedFile{
			Path:   path,
			Lang:   lang,
			Source: src,
			Size:   size,
			Err:    err,
			IsRead: true,
		}
	}

	return ParsedFile{
		Path:   path,
		Tree:   tree,
		Lang:   lang,
		Source: src,
		Size:   size,
		IsRead: true,
	}
}
