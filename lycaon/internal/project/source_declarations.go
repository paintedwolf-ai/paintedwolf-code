package project

import (
	"context"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
)

// DeclarationMatch selects how discovery matches a pattern in file content.
type DeclarationMatch int

const (
	// DeclarationMatchWholeWord matches the pattern as a whole word, ignoring case.
	DeclarationMatchWholeWord DeclarationMatch = iota
	// DeclarationMatchSubstring matches the pattern anywhere in a line, ignoring case.
	DeclarationMatchSubstring
	// DeclarationMatchRegexp matches the pattern as a case-sensitive regular expression.
	DeclarationMatchRegexp
)

func (m DeclarationMatch) String() string {
	switch m {
	case DeclarationMatchWholeWord:
		return "whole_word"
	case DeclarationMatchSubstring:
		return "substring"
	case DeclarationMatchRegexp:
		return "regexp"
	default:
		return "unknown"
	}
}

// DeclarationSearchRoot is one root discovery scans.
type DeclarationSearchRoot struct {
	ID   string
	Path string
}

// DeclarationSearchQuery is one content-discovery pass over project roots.
type DeclarationSearchQuery struct {
	ProjectID string
	Roots     []DeclarationSearchRoot
	Pattern   string
	Match     DeclarationMatch
	// ExcludeDirs carries catalog-provided dependency and build directories.
	ExcludeDirs []string
	HitCap      int
	// Wall bounds the scan when positive.
	Wall time.Duration
}

// DeclarationSearchHit is one matching content line.
type DeclarationSearchHit struct {
	RootID  string
	Path    string
	Snippet string
}

// DeclarationSearch finds content lines that may hold declarations. partial
// reports caps, time limits, and coverage gaps.
type DeclarationSearch func(ctx context.Context, query DeclarationSearchQuery) (hits []DeclarationSearchHit, partial bool, err error)

// declarationFile is one file discovery nominated for AST confirmation.
type declarationFile struct {
	rootID string
	path   string
	// snippets are the file's matching lines in discovery order.
	snippets []string
}

type declarationFileKey struct{ rootID, path string }

// declarationFilesFromHits groups hits by file, keeping first-hit order.
func declarationFilesFromHits(hits []DeclarationSearchHit) []declarationFile {
	files := make([]declarationFile, 0, 32)
	index := map[declarationFileKey]int{}
	for _, hit := range hits {
		rel := filepathToSlash(hit.Path)
		if rel == "" {
			continue
		}
		key := declarationFileKey{rootID: hit.RootID, path: rel}
		at, seen := index[key]
		if !seen {
			at = len(files)
			index[key] = at
			files = append(files, declarationFile{rootID: hit.RootID, path: rel})
		}
		files[at].snippets = append(files[at].snippets, hit.Snippet)
	}
	return files
}

// declarationParseWorkers bounds concurrent per-file parsing.
func declarationParseWorkers(files int) int {
	n := min(runtime.NumCPU(), 8)
	return max(1, min(n, files))
}

// parseDeclarations outlines files concurrently; pick projects each file's
// declarations, and results keep file order. incomplete reports files that
// could not be read or analyzed.
func parseDeclarations[T any](
	ctx context.Context,
	p *Project,
	files []declarationFile,
	pick func(file declarationFile, symbols []SourceSymbol, content string) []T,
) (perFile [][]T, incomplete bool) {
	perFile = make([][]T, len(files))
	var wg sync.WaitGroup
	var sawIncomplete atomic.Bool
	sem := make(chan struct{}, declarationParseWorkers(len(files)))
	for i, file := range files {
		if ctx.Err() != nil {
			sawIncomplete.Store(true)
			break
		}
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			symbols, content, ok := declarationsInFile(ctx, p, file.rootID, file.path)
			if !ok {
				sawIncomplete.Store(true)
				return
			}
			perFile[i] = pick(file, symbols, content)
		}()
	}
	wg.Wait()
	return perFile, sawIncomplete.Load()
}

func declarationsInFile(ctx context.Context, p *Project, rootID, rel string) ([]SourceSymbol, string, bool) {
	if err := ctx.Err(); err != nil {
		return nil, "", false
	}
	read, err := ReadProjectSource(p, SourceReadRequest{Path: rel, RootID: rootID})
	if err != nil {
		return nil, "", false
	}
	syms, err := SourceSymbolsForContent(ctx, rel, []byte(read.Content))
	if err != nil || ctx.Err() != nil {
		return nil, "", false
	}
	if len(syms) > SourceSymbolsMaxEntries {
		syms = syms[:SourceSymbolsMaxEntries]
	}
	return syms, read.Content, true
}
