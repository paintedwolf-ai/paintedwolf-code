package projectsource

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
	// Continue requests file nominations from a caller-owned bounded frontier.
	Continue  bool
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

// DeclarationSearch finds content that may hold declarations and preserves
// independent candidate limits and structured source coverage gaps.
type DeclarationSearch func(ctx context.Context, query DeclarationSearchQuery) (hits []DeclarationSearchHit, coverage DeclarationCoverage, err error)

// DeclarationFile is one file discovery nominated for AST confirmation.
type DeclarationFile struct {
	RootID string
	Path   string
	// Snippets are the file's matching lines in discovery order.
	Snippets []string
}

type DeclarationFileKey struct{ RootID, Path string }

// DeclarationFilesFromHits groups hits by file, keeping first-hit order.
func DeclarationFilesFromHits(hits []DeclarationSearchHit) []DeclarationFile {
	files := make([]DeclarationFile, 0, 32)
	index := map[DeclarationFileKey]int{}
	for _, hit := range hits {
		rel := filepathToSlash(hit.Path)
		if rel == "" {
			continue
		}
		key := DeclarationFileKey{RootID: hit.RootID, Path: rel}
		at, seen := index[key]
		if !seen {
			at = len(files)
			index[key] = at
			files = append(files, DeclarationFile{RootID: hit.RootID, Path: rel})
		}
		files[at].Snippets = append(files[at].Snippets, hit.Snippet)
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
	p ProjectSource,
	files []DeclarationFile,
	pick func(file DeclarationFile, symbols []SourceSymbol, content string) []T,
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
			symbols, content, ok := ReadSourceDeclarations(ctx, p, file.RootID, file.Path)
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

// ReadSourceDeclarations reads and analyzes one root-relative source file.
func ReadSourceDeclarations(ctx context.Context, p ProjectSource, rootID, rel string) ([]SourceSymbol, string, bool) {
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

type DeclarationGapReason string

const (
	DeclarationPending           DeclarationGapReason = "symbol_pending"
	DeclarationTimeBudget        DeclarationGapReason = "time_budget"
	DeclarationSymbolBudget      DeclarationGapReason = "symbol_budget"
	DeclarationFilesSkipped      DeclarationGapReason = "files_skipped"
	DeclarationCatalogWarming    DeclarationGapReason = "catalog_warming"
	DeclarationCatalogIncomplete DeclarationGapReason = "catalog_incomplete"
	DeclarationCatalogRefreshing DeclarationGapReason = "catalog_refreshing"
	DeclarationIndexWarming      DeclarationGapReason = "index_warming"
)

// DeclarationGap preserves a discovery failure without interpreting diagnostic prose.
// Reason is the search subsystem's coverage code; Count and Limit keep its units.
type DeclarationGap struct {
	Reason  DeclarationGapReason
	Count   int
	Limit   int
	Message string
}

// DeclarationCoverage distinguishes a capped candidate set from source coverage gaps.
type DeclarationCoverage struct {
	Limited bool
	Gaps    []DeclarationGap
}

func (c DeclarationCoverage) Incomplete() bool { return c.Limited || len(c.Gaps) != 0 }
