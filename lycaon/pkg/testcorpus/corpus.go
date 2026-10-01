// Package testcorpus loads deterministic source snapshots for structural tests.
package testcorpus

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// Options controls which files Load includes.
type Options struct {
	// Extensions filters by filename extension; empty includes every file.
	Extensions []string
	// SkipDirectories contains basenames to omit; empty uses the default set.
	SkipDirectories []string
}

var defaultSkipDirectories = [...]string{".git", "node_modules", "vendor"}

// File is an immutable source snapshot with a slash-separated relative path.
type File struct {
	Path string
	Rel  string
	data []byte
}

// Bytes returns a copy of the file contents.
func (f File) Bytes() []byte { return append([]byte(nil), f.data...) }

// Text returns the file contents as a string.
func (f File) Text() string { return string(f.data) }

// Corpus is a deterministic snapshot of files below Root.
type Corpus struct {
	Root  string
	files []File
	byRel map[string]int
}

// Load returns selected files in lexical relative-path order.
func Load(root string, opts Options) (*Corpus, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve corpus root: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("stat corpus root: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("corpus root is not a directory: %s", abs)
	}

	exts := normalizedSet(opts.Extensions, true)
	skips := opts.SkipDirectories
	if len(skips) == 0 {
		skips = defaultSkipDirectories[:]
	}
	skipDirs := normalizedSet(skips, false)
	corpus := &Corpus{Root: abs, byRel: map[string]int{}}
	corpusRoot, err := os.OpenRoot(abs)
	if err != nil {
		return nil, fmt.Errorf("open corpus root %s: %w", abs, err)
	}
	defer func() { _ = corpusRoot.Close() }()
	err = filepath.WalkDir(abs, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if path != abs {
				if _, skip := skipDirs[entry.Name()]; skip {
					return filepath.SkipDir
				}
			}
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("stat corpus entry %s: %w", path, err)
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		if len(exts) != 0 {
			if _, include := exts[filepath.Ext(entry.Name())]; !include {
				return nil
			}
		}
		rel, err := filepath.Rel(abs, path)
		if err != nil {
			return fmt.Errorf("relativize corpus file %s: %w", path, err)
		}
		data, err := corpusRoot.ReadFile(rel)
		if err != nil {
			return fmt.Errorf("read corpus file %s: %w", path, err)
		}
		rel = filepath.ToSlash(rel)
		corpus.byRel[rel] = len(corpus.files)
		corpus.files = append(corpus.files, File{Path: path, Rel: rel, data: data})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk corpus root %s: %w", abs, err)
	}
	return corpus, nil
}

// Files returns the corpus files.
func (c *Corpus) Files() []File {
	return append([]File(nil), c.files...)
}

// File returns the file with the given slash-separated relative path.
func (c *Corpus) File(rel string) (File, bool) {
	rel = strings.TrimPrefix(filepath.ToSlash(filepath.Clean(rel)), "./")
	i, ok := c.byRel[rel]
	if !ok {
		return File{}, false
	}
	return c.files[i], true
}

// Under returns files at or below rel, in corpus order.
func (c *Corpus) Under(rel string) []File {
	prefix := cleanRelativePrefix(rel)
	if prefix == "" {
		return c.Files()
	}
	out := make([]File, 0)
	for _, file := range c.files {
		if file.Rel == prefix || strings.HasPrefix(file.Rel, prefix+"/") {
			out = append(out, file)
		}
	}
	return out
}

// Select returns files accepted by keep, in corpus order.
func (c *Corpus) Select(keep func(File) bool) []File {
	out := make([]File, 0)
	for _, file := range c.files {
		if keep(file) {
			out = append(out, file)
		}
	}
	return out
}

func cleanRelativePrefix(rel string) string {
	clean := filepath.ToSlash(filepath.Clean(rel))
	if clean == "." {
		return ""
	}
	return strings.Trim(clean, "/")
}

func normalizedSet(values []string, extensions bool) map[string]struct{} {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if extensions && !strings.HasPrefix(value, ".") {
			value = "." + value
		}
		set[value] = struct{}{}
	}
	return set
}

// Loader caches corpora by absolute root and normalized options.
type Loader struct {
	entries sync.Map
}

type cacheEntry struct {
	once   sync.Once
	corpus *Corpus
	err    error
}

// Load returns one cached corpus for root and opts.
func (l *Loader) Load(root string, opts Options) (*Corpus, error) {
	key, err := cacheKey(root, opts)
	if err != nil {
		return nil, err
	}
	raw, _ := l.entries.LoadOrStore(key, &cacheEntry{})
	entry := raw.(*cacheEntry)
	entry.once.Do(func() {
		entry.corpus, entry.err = Load(root, opts)
	})
	return entry.corpus, entry.err
}

func cacheKey(root string, opts Options) (string, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve corpus root: %w", err)
	}
	exts := normalizedValues(opts.Extensions, true)
	skips := opts.SkipDirectories
	if len(skips) == 0 {
		skips = defaultSkipDirectories[:]
	}
	skips = normalizedValues(skips, false)
	return abs + "\x00" + strings.Join(exts, "\x00") + "\x01" + strings.Join(skips, "\x00"), nil
}

func normalizedValues(values []string, extensions bool) []string {
	set := normalizedSet(values, extensions)
	out := make([]string, 0, len(set))
	for value := range set {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}
