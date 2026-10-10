package projectsource

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// Project symbol search bounds. The limit sets Limited; the other bounds set
// Incomplete when they stop discovery.
const (
	SourceSymbolSearchMinQueryRunes = 2
	SourceSymbolSearchDefaultLimit  = 50
	SourceSymbolSearchMaxLimit      = 200
	// SymbolSearchHitCap bounds content lines per discovery pass.
	SymbolSearchHitCap = 1000
	// SymbolSearchFileCap bounds files outlined per request.
	SymbolSearchFileCap = 48
	// SymbolSearchWall bounds the substring and whole-word passes together.
	SymbolSearchWall = 750 * time.Millisecond
	// SymbolAbbreviationWall bounds the abbreviation pass, which scans content
	// without a literal to narrow it.
	SymbolAbbreviationWall = 250 * time.Millisecond
)

// SourceSymbolSearchRequest names declarations to find in a project's roots.
type SourceSymbolSearchRequest struct {
	Query string
	// RootIDs narrows the search to these roots; empty searches every root.
	RootIDs []string
	Limit   int
	// CaseSensitive keeps names whose matched characters spell Query exactly.
	CaseSensitive bool
	// Exact keeps whole-name matches only.
	Exact bool
	// ExcludeDirs carries catalog-provided dependency and build directories.
	ExcludeDirs []string
	// Wall bounds the substring and whole-word passes; zero uses SymbolSearchWall.
	Wall time.Duration
	// AbbreviationWall bounds the abbreviation pass; zero uses SymbolAbbreviationWall.
	AbbreviationWall time.Duration
}

// SourceSymbolMatch is one declaration whose name matches the query.
type SourceSymbolMatch struct {
	RootID string
	Path   string
	Line   int
	Name   string
	Kind   SourceSymbolKind
	// Highlights are code point ranges of Name the query matched.
	Highlights []SourceTextRange
	// Signature is the declaration's source line, trimmed.
	Signature string
	tier      symbolMatchTier
	rootOrder int
}

// MatchRank orders how the name answers the query, 0 strongest: the query's
// own spelling, the whole name, a prefix, a word start, an abbreviation, then
// a substring.
func (m SourceSymbolMatch) MatchRank() int { return int(m.tier) }

// SourceSymbolSearchResult is the ranked match list for one query.
type SourceSymbolSearchResult struct {
	Symbols []SourceSymbolMatch
	// Limited means the limit cut matches that were found.
	Limited bool
	// Incomplete means a line cap, clock, file budget, unreadable file, or
	// catalog coverage gap stopped discovery, so more matches may exist.
	Incomplete bool
	// Passes say where discovery and outlining spent the request's time.
	Passes []SymbolSearchPass
}

// SymbolSearchPass reports one discovery pass and the outlining that followed it.
type SymbolSearchPass struct {
	Match     DeclarationMatch
	Hits      int
	Files     int
	Partial   bool
	Discovery time.Duration
	Outline   time.Duration
}

// SearchProjectSourceSymbols finds declarations by name across project roots.
// Content discovery nominates files, their outlines confirm declarations, and
// names rank exact, prefix, word start, abbreviation, then substring. Headings
// are prose, not declarations, and never match.
func SearchProjectSourceSymbols(
	ctx context.Context,
	p ProjectSource,
	req SourceSymbolSearchRequest,
	search DeclarationSearch,
) (SourceSymbolSearchResult, error) {
	query := strings.TrimSpace(req.Query)
	if utf8.RuneCountInString(query) < SourceSymbolSearchMinQueryRunes {
		return SourceSymbolSearchResult{Symbols: []SourceSymbolMatch{}}, nil
	}
	if p == nil || len(p.SourceRoots()) == 0 {
		return SourceSymbolSearchResult{}, ErrSourceNoRoot
	}
	if search == nil {
		return SourceSymbolSearchResult{}, errors.New("declaration search is required")
	}
	roots, err := symbolSearchRoots(p, req.RootIDs)
	if err != nil {
		return SourceSymbolSearchResult{}, err
	}
	limit := req.Limit
	if limit <= 0 {
		limit = SourceSymbolSearchDefaultLimit
	}
	limit = min(limit, SourceSymbolSearchMaxLimit)
	wall, abbreviationWall := req.Wall, req.AbbreviationWall
	if wall <= 0 {
		wall = SymbolSearchWall
	}
	if abbreviationWall <= 0 {
		abbreviationWall = SymbolAbbreviationWall
	}

	run := &symbolSearchRun{
		p:             p,
		search:        search,
		matcher:       newSymbolNameMatcher(query),
		caseSensitive: req.CaseSensitive,
		exact:         req.Exact,
		roots:         roots,
		excludes:      req.ExcludeDirs,
		parsed:        map[declarationFileKey]struct{}{},
		filesLeft:     SymbolSearchFileCap,
	}
	deadline := time.Now().Add(wall)
	firstPass := DeclarationMatchSubstring
	if req.Exact {
		firstPass = DeclarationMatchWholeWord
	}
	hits, capped, err := run.discover(ctx, query, firstPass, deadline)
	if err != nil {
		return SourceSymbolSearchResult{}, err
	}
	// A capped substring pass may have stopped before an exact name.
	if capped && !req.Exact {
		exact, _, err := run.discover(ctx, query, DeclarationMatchWholeWord, deadline)
		if err != nil {
			return SourceSymbolSearchResult{}, err
		}
		hits = append(hits, exact...)
	}
	run.outline(ctx, hits)
	// An exact name skips the abbreviation tier. Better tiers that fill the
	// page skip it too, which reports as a limit.
	fullPage := false
	if pattern, ok := symbolHumpPattern(query); ok && !req.Exact {
		switch {
		case run.countBetterThan(symbolTierPrefix) > 0:
		case run.countBetterThan(symbolTierHump) >= limit:
			fullPage = true
		default:
			abbreviated, _, err := run.discover(ctx, pattern, DeclarationMatchRegexp, time.Now().Add(abbreviationWall))
			if err != nil {
				return SourceSymbolSearchResult{}, err
			}
			run.outline(ctx, abbreviated)
		}
	}

	matches := run.matches
	sort.SliceStable(matches, func(i, j int) bool { return symbolMatchLess(matches[i], matches[j]) })
	limited := fullPage || len(matches) > limit
	if len(matches) > limit {
		matches = matches[:limit]
	}
	if matches == nil {
		matches = []SourceSymbolMatch{}
	}
	return SourceSymbolSearchResult{Symbols: matches, Limited: limited, Incomplete: run.incomplete, Passes: run.passes}, nil
}

type symbolSearchRoot struct {
	DeclarationSearchRoot
	order int
}

// symbolSearchRoots selects every root, or the roots rootIDs names, in the
// project's root order.
func symbolSearchRoots(p ProjectSource, rootIDs []string) ([]symbolSearchRoot, error) {
	wanted := map[string]bool{}
	for _, id := range rootIDs {
		if id = strings.TrimSpace(id); id != "" {
			wanted[id] = true
		}
	}
	out := make([]symbolSearchRoot, 0, len(p.SourceRoots()))
	for i, root := range p.SourceRoots() {
		if len(wanted) > 0 && !wanted[root.ID] {
			continue
		}
		out = append(out, symbolSearchRoot{DeclarationSearchRoot: DeclarationSearchRoot{ID: root.ID, Path: root.Path}, order: i})
	}
	if len(out) == 0 {
		return nil, ErrSourceNoRoot
	}
	return out, nil
}

// symbolSearchRun shares the file budget across passes.
type symbolSearchRun struct {
	p             ProjectSource
	search        DeclarationSearch
	matcher       symbolNameMatcher
	caseSensitive bool
	exact         bool
	roots         []symbolSearchRoot
	excludes      []string
	parsed        map[declarationFileKey]struct{}
	filesLeft     int
	matches       []SourceSymbolMatch
	incomplete    bool
	passes        []SymbolSearchPass
}

// discover runs one content pass until deadline. capped reports that it
// stopped at its line cap.
func (r *symbolSearchRun) discover(ctx context.Context, pattern string, match DeclarationMatch, deadline time.Time) (hits []DeclarationSearchHit, capped bool, err error) {
	wall := time.Until(deadline)
	if wall <= 0 || r.filesLeft <= 0 || ctx.Err() != nil {
		r.incomplete = true
		return nil, false, nil //nolint:nilerr // an exhausted budget stops the search instead of failing it
	}
	roots := make([]DeclarationSearchRoot, 0, len(r.roots))
	for _, root := range r.roots {
		roots = append(roots, root.DeclarationSearchRoot)
	}
	started := time.Now()
	hits, partial, err := r.search(ctx, DeclarationSearchQuery{
		ProjectID:   r.p.SourceID(),
		Roots:       roots,
		Pattern:     pattern,
		Match:       match,
		ExcludeDirs: append([]string(nil), r.excludes...),
		HitCap:      SymbolSearchHitCap,
		Wall:        wall,
	})
	if err != nil {
		if ctx.Err() != nil {
			r.incomplete = true
			return nil, false, nil //nolint:nilerr // cancellation stops the search instead of failing it
		}
		return nil, false, err
	}
	r.passes = append(r.passes, SymbolSearchPass{Match: match, Hits: len(hits), Partial: partial, Discovery: time.Since(started)})
	r.incomplete = r.incomplete || partial
	return hits, len(hits) >= SymbolSearchHitCap, nil
}

// outline reads the files hits nominate that are not yet read, within the
// file budget, and keeps their matching declarations.
func (r *symbolSearchRun) outline(ctx context.Context, hits []DeclarationSearchHit) {
	files := r.unparsed(declarationFilesFromHits(hits))
	if len(files) > r.filesLeft {
		r.incomplete = true
		files = files[:r.filesLeft]
	}
	r.filesLeft -= len(files)
	for _, file := range files {
		r.parsed[declarationFileKey{rootID: file.rootID, path: file.path}] = struct{}{}
	}
	started := time.Now()
	perFile, incomplete := parseDeclarations(ctx, r.p, files, r.pick)
	if n := len(r.passes); n > 0 {
		r.passes[n-1].Files, r.passes[n-1].Outline = len(files), time.Since(started)
	}
	r.incomplete = r.incomplete || incomplete
	for _, list := range perFile {
		r.matches = append(r.matches, list...)
	}
}

// unparsed orders new files by the best identifier their matched lines hold,
// so the file budget reaches likely declarations first.
func (r *symbolSearchRun) unparsed(files []declarationFile) []declarationFile {
	type ranked struct {
		file  declarationFile
		tier  symbolMatchTier
		depth int
	}
	candidates := make([]ranked, 0, len(files))
	for _, file := range files {
		if _, done := r.parsed[declarationFileKey{rootID: file.rootID, path: file.path}]; done {
			continue
		}
		tier := symbolTierNone
		for _, snippet := range file.snippets {
			tier = min(tier, r.matcher.bestTokenTier(snippet))
		}
		candidates = append(candidates, ranked{file: file, tier: tier, depth: strings.Count(file.path, "/")})
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].tier != candidates[j].tier {
			return candidates[i].tier < candidates[j].tier
		}
		return candidates[i].depth < candidates[j].depth
	})
	out := make([]declarationFile, len(candidates))
	for i, candidate := range candidates {
		out[i] = candidate.file
	}
	return out
}

func (r *symbolSearchRun) pick(file declarationFile, symbols []SourceSymbol, content string) []SourceSymbolMatch {
	var out []SourceSymbolMatch
	seen := map[SourceSymbol]struct{}{}
	for _, sym := range symbols {
		if sym.Kind == SourceSymbolKindHeading {
			continue
		}
		if _, dup := seen[sym]; dup {
			continue
		}
		seen[sym] = struct{}{}
		tier, highlights := r.matcher.match(sym.Name)
		if !r.keeps(sym.Name, tier, highlights) {
			continue
		}
		out = append(out, SourceSymbolMatch{
			RootID:     file.rootID,
			Path:       file.path,
			Line:       sym.Line,
			Name:       sym.Name,
			Kind:       sym.Kind,
			Highlights: highlights,
			Signature:  lineSnippet(content, sym.Line),
			tier:       tier,
			rootOrder:  r.rootOrder(file.rootID),
		})
	}
	return out
}

// keeps applies the request's exactness and case to one match.
func (r *symbolSearchRun) keeps(name string, tier symbolMatchTier, highlights []SourceTextRange) bool {
	switch {
	case tier == symbolTierNone:
		return false
	case r.caseSensitive && r.exact:
		return tier == symbolTierExactCase
	case r.exact:
		return tier <= symbolTierExact
	case r.caseSensitive:
		return r.matcher.spelledExactly(name, highlights)
	default:
		return true
	}
}

func (r *symbolSearchRun) rootOrder(rootID string) int {
	for _, root := range r.roots {
		if root.ID == rootID {
			return root.order
		}
	}
	return len(r.p.SourceRoots())
}

func (r *symbolSearchRun) countBetterThan(tier symbolMatchTier) int {
	n := 0
	for _, match := range r.matches {
		if match.tier < tier {
			n++
		}
	}
	return n
}

// symbolKindRank orders declarations of one tier: types and classes, then
// functions, methods, and constants.
func symbolKindRank(kind SourceSymbolKind) int {
	switch kind {
	case SourceSymbolKindType, SourceSymbolKindClass:
		return 0
	case SourceSymbolKindFunction:
		return 1
	case SourceSymbolKindMethod:
		return 2
	case SourceSymbolKindConstant:
		return 3
	default:
		return 4
	}
}

func symbolMatchLess(a, b SourceSymbolMatch) bool {
	if a.tier != b.tier {
		return a.tier < b.tier
	}
	if ak, bk := symbolKindRank(a.Kind), symbolKindRank(b.Kind); ak != bk {
		return ak < bk
	}
	if ad, bd := strings.Count(a.Path, "/"), strings.Count(b.Path, "/"); ad != bd {
		return ad < bd
	}
	if a.Path != b.Path {
		return a.Path < b.Path
	}
	if a.rootOrder != b.rootOrder {
		return a.rootOrder < b.rootOrder
	}
	if a.Line != b.Line {
		return a.Line < b.Line
	}
	return a.Name < b.Name
}
