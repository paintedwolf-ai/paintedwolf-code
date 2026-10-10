package symbolsearch

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/projectsource"
)

// Project symbol search bounds. The limit sets Limited; the other bounds set
// Incomplete when they stop discovery.
const (
	MinQueryRunes = 2
	DefaultLimit  = 50
	MaxLimit      = 200
	// DiscoveryFileCap bounds nominated files per discovery batch.
	DiscoveryFileCap = 1000
	// OutlineFileCap bounds files outlined per request.
	OutlineFileCap = 48
	// DiscoveryWall is the default literal discovery allocation.
	DiscoveryWall = 750 * time.Millisecond
	// AbbreviationWall bounds the abbreviation pass, which scans content
	// without a literal to narrow it.
	AbbreviationWall = 250 * time.Millisecond
)

// Request names declarations to find in a project's roots.
type Request struct {
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
	// Admits applies the caller's query filters before retaining or capping results.
	Admits func(rootID, path, name string) bool
	// Wall bounds literal discovery; zero uses DiscoveryWall.
	Wall time.Duration
	// AbbreviationWall bounds the abbreviation pass; zero uses AbbreviationWall.
	AbbreviationWall time.Duration
	OutlineWall      time.Duration
	// Progress retains bounded work between requests with the same source identity.
	Progress *Progress
}

// Match is one declaration whose name matches the query.
type Match struct {
	RootID string
	Path   string
	Line   int
	Name   string
	Kind   projectsource.SourceSymbolKind
	// Highlights are code point ranges of Name the query matched.
	Highlights []projectsource.SourceTextRange
	// Signature is the declaration's source line, trimmed.
	Signature string
	tier      symbolMatchTier
	rootOrder int
}

// MatchRank orders how the name answers the query, 0 strongest: the query's
// own spelling, the whole name, a prefix, a word start, an abbreviation, then
// a substring.
func (m Match) MatchRank() int { return int(m.tier) }

// Result is the ranked match list for one query.
type Result struct {
	Symbols []Match
	// Limited means the limit cut matches that were found.
	Limited bool
	// Incomplete means a line cap, clock, file budget, unreadable file, or
	// catalog coverage gap stopped discovery, so more matches may exist.
	Incomplete bool
	Coverage   projectsource.DeclarationCoverage
	// Passes say where discovery and outlining spent the request's time.
	Passes []Pass
}

// Pass reports one discovery pass and the outlining that followed it.
type Pass struct {
	Match     projectsource.DeclarationMatch
	Hits      int
	Files     int
	Partial   bool
	Discovery time.Duration
	Outline   time.Duration
}

// Run finds declarations by name across project roots.
// Content discovery nominates files, their outlines confirm declarations, and
// names rank exact, prefix, word start, abbreviation, then substring. Headings
// are prose, not declarations, and never match.
func Run(
	ctx context.Context,
	p projectsource.ProjectSource,
	req Request,
	search projectsource.DeclarationSearch,
) (Result, error) {
	query := strings.TrimSpace(req.Query)
	if utf8.RuneCountInString(query) < MinQueryRunes {
		return Result{Symbols: []Match{}}, nil
	}
	if p == nil || len(p.SourceRoots()) == 0 {
		return Result{}, projectsource.ErrSourceNoRoot
	}
	if search == nil {
		return Result{}, errors.New("declaration search is required")
	}
	roots, err := symbolSearchRoots(p, req.RootIDs)
	if err != nil {
		return Result{}, err
	}
	limit := req.Limit
	if limit <= 0 {
		limit = DefaultLimit
	}
	limit = min(limit, MaxLimit)
	wall, abbreviationWall := req.Wall, req.AbbreviationWall
	if wall <= 0 {
		wall = DiscoveryWall
	}
	if abbreviationWall <= 0 {
		abbreviationWall = AbbreviationWall
	}

	progress := req.Progress
	if progress == nil {
		progress = &Progress{}
	}
	run := &symbolSearchRun{
		p: p, search: search, matcher: newSymbolNameMatcher(query),
		caseSensitive: req.CaseSensitive, exact: req.Exact, roots: roots,
		admits:   req.Admits,
		excludes: req.ExcludeDirs, filesLeft: OutlineFileCap,
		matches: append([]Match(nil), progress.matches...),
	}
	outlineWall := req.OutlineWall
	if outlineWall <= 0 {
		outlineWall = wall
	}
	fullPage, err := run.advance(ctx, query, limit, wall, abbreviationWall, outlineWall, progress, req.Progress != nil)
	if err != nil {
		run.saveProgress(progress)
		return Result{}, err
	}

	matches := run.matches
	sort.SliceStable(matches, func(i, j int) bool { return symbolMatchLess(matches[i], matches[j]) })
	limited := fullPage || len(matches) > limit
	if len(matches) > limit {
		matches = matches[:limit]
	}
	if matches == nil {
		matches = []Match{}
	}
	return Result{Symbols: matches, Limited: limited, Incomplete: run.incomplete, Passes: run.passes, Coverage: run.coverage}, nil
}

type symbolSearchRoot struct {
	projectsource.DeclarationSearchRoot
	order int
}

// symbolSearchRoots selects every root, or the roots rootIDs names, in the
// project's root order.
func symbolSearchRoots(p projectsource.ProjectSource, rootIDs []string) ([]symbolSearchRoot, error) {
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
		out = append(out, symbolSearchRoot{DeclarationSearchRoot: projectsource.DeclarationSearchRoot{ID: root.ID, Path: root.Path}, order: i})
	}
	if len(out) == 0 {
		return nil, projectsource.ErrSourceNoRoot
	}
	return out, nil
}

// symbolSearchRun shares the file budget across passes.
type symbolSearchRun struct {
	p             projectsource.ProjectSource
	search        projectsource.DeclarationSearch
	matcher       symbolNameMatcher
	caseSensitive bool
	exact         bool
	roots         []symbolSearchRoot
	excludes      []string
	admits        func(rootID, path, name string) bool
	filesLeft     int
	matches       []Match
	incomplete    bool
	coverage      projectsource.DeclarationCoverage
	passes        []Pass
}

func (r *symbolSearchRun) pick(file projectsource.DeclarationFile, symbols []projectsource.SourceSymbol, content string) []Match {
	var out []Match
	seen := map[projectsource.SourceSymbol]struct{}{}
	for _, sym := range symbols {
		if sym.Kind == projectsource.SourceSymbolKindHeading {
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
		if r.admits != nil && !r.admits(file.RootID, file.Path, sym.Name) {
			continue
		}
		out = append(out, Match{
			RootID:     file.RootID,
			Path:       file.Path,
			Line:       sym.Line,
			Name:       sym.Name,
			Kind:       sym.Kind,
			Highlights: highlights,
			Signature:  symbolSignature(content, sym.Line),
			tier:       tier,
			rootOrder:  r.rootOrder(file.RootID),
		})
	}
	return out
}

// keeps applies the request's exactness and case to one match.
func (r *symbolSearchRun) keeps(name string, tier symbolMatchTier, highlights []projectsource.SourceTextRange) bool {
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
func symbolKindRank(kind projectsource.SourceSymbolKind) int {
	switch kind {
	case projectsource.SourceSymbolKindType, projectsource.SourceSymbolKindClass:
		return 0
	case projectsource.SourceSymbolKindFunction:
		return 1
	case projectsource.SourceSymbolKindMethod:
		return 2
	case projectsource.SourceSymbolKindConstant:
		return 3
	default:
		return 4
	}
}

func symbolMatchLess(a, b Match) bool {
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

func symbolSignature(content string, line int) string {
	snippet := projectsource.DeclarationLine(content, line)
	if len(snippet) <= 512 {
		return snippet
	}
	end := 512
	for end > 0 && !utf8.RuneStart(snippet[end]) {
		end--
	}
	return snippet[:end] + "…"
}
