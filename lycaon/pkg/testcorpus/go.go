package testcorpus

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"sync"
)

// GoFile is one parsed Go source file and its original source snapshot.
type GoFile struct {
	File
	AST    *ast.File
	IsTest bool
}

// GoCorpus is a source corpus parsed with one shared token.FileSet.
type GoCorpus struct {
	Root  string
	Fset  *token.FileSet
	files []GoFile
}

// ParseGo parses every .go file in corpus with mode.
func ParseGo(corpus *Corpus, mode parser.Mode) (*GoCorpus, error) {
	parsed := &GoCorpus{Root: corpus.Root, Fset: token.NewFileSet()}
	for _, source := range corpus.files {
		if filepath.Ext(source.Rel) != ".go" {
			continue
		}
		file, err := parser.ParseFile(parsed.Fset, source.Path, source.data, mode)
		if err != nil {
			return nil, fmt.Errorf("parse corpus file %s: %w", source.Path, err)
		}
		parsed.files = append(parsed.files, GoFile{
			File:   source,
			AST:    file,
			IsTest: strings.HasSuffix(source.Rel, "_test.go"),
		})
	}
	return parsed, nil
}

// LoadGo loads and parses all Go files below root.
func LoadGo(root string, opts Options, mode parser.Mode) (*GoCorpus, error) {
	opts.Extensions = []string{".go"}
	corpus, err := Load(root, opts)
	if err != nil {
		return nil, err
	}
	return ParseGo(corpus, mode)
}

// GoLoader caches parsed corpora; returned ASTs are shared.
type GoLoader struct {
	entries sync.Map
}

type goCacheEntry struct {
	once   sync.Once
	corpus *GoCorpus
	err    error
}

// Load returns one cached parsed corpus for root, opts, and mode.
func (l *GoLoader) Load(root string, opts Options, mode parser.Mode) (*GoCorpus, error) {
	opts.Extensions = []string{".go"}
	key, err := cacheKey(root, opts)
	if err != nil {
		return nil, err
	}
	key += fmt.Sprintf("\x02%d", mode)
	raw, _ := l.entries.LoadOrStore(key, &goCacheEntry{})
	entry := raw.(*goCacheEntry)
	entry.once.Do(func() {
		entry.corpus, entry.err = LoadGo(root, opts, mode)
	})
	return entry.corpus, entry.err
}

// Files returns all parsed Go files.
func (c *GoCorpus) Files() []GoFile {
	return append([]GoFile(nil), c.files...)
}

// Production returns non-test Go files.
func (c *GoCorpus) Production() []GoFile {
	return c.Select(func(file GoFile) bool { return !file.IsTest })
}

// Tests returns *_test.go files.
func (c *GoCorpus) Tests() []GoFile {
	return c.Select(func(file GoFile) bool { return file.IsTest })
}

// Under returns parsed files at or below rel.
func (c *GoCorpus) Under(rel string) []GoFile {
	prefix := cleanRelativePrefix(rel)
	return c.Select(func(file GoFile) bool {
		return prefix == "" || file.Rel == prefix || strings.HasPrefix(file.Rel, prefix+"/")
	})
}

// Select returns parsed files accepted by keep, in corpus order.
func (c *GoCorpus) Select(keep func(GoFile) bool) []GoFile {
	out := make([]GoFile, 0)
	for _, file := range c.files {
		if keep(file) {
			out = append(out, file)
		}
	}
	return out
}
