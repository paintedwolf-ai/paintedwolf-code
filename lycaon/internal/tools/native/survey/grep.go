package survey

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/lycaon/lycaon/internal/litprefilter"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/structrewrite"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/sourceview"
	"github.com/lycaon/lycaon/internal/tools/native/toolkit"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
	"github.com/lycaon/lycaon/internal/tools/readcaps"
	"github.com/lycaon/lycaon/internal/tools/safecmd"
	"github.com/lycaon/lycaon/internal/tsparse"
)

// hostGrepMaxFileBytes is mutable for small cap tests.
var hostGrepMaxFileBytes = readcaps.MaxFileBytes

var (
	errGrepMatchCap = errors.New("grep match cap reached")
)

// grepMetavarRe excludes anchors and numeric backreferences.
var grepMetavarRe = regexp.MustCompile(`\$(\$\$)?[A-Za-z_]`)

// GrepTool searches text or syntax structure under a path.
type GrepTool struct {
	Boundary *sandbox.Boundary
	Catalog  *sourcecatalog.Catalog
}

type grepMatch struct {
	Path          string            `json:"path"`
	Line          int               `json:"line"`
	Content       string            `json:"content"`
	Match         string            `json:"match,omitempty"`
	Count         int               `json:"-"`
	Bindings      map[string]string `json:"bindings,omitempty"`
	ContextBefore []string          `json:"context_before,omitempty"`
	ContextAfter  []string          `json:"context_after,omitempty"`
	// Structured identity of the matched file; never serialized.
	rootID   string `json:"-"`
	scopeRel string `json:"-"`
}

type grepResponse struct {
	View                string          `json:"view,omitempty"`
	Matches             []grepMatch     `json:"matches,omitempty"`
	Offset              int             `json:"offset"`
	MaxMatches          int             `json:"max_matches,omitempty"`
	Truncated           bool            `json:"truncated"`
	NextOffset          *int            `json:"next_offset,omitempty"`
	Distribution        []grepDirCount  `json:"distribution,omitempty"`
	Note                string          `json:"note,omitempty"`
	Highlights          []readHighlight `json:"highlights,omitempty"`
	Gloss               []readGlossLine `json:"gloss,omitempty"`
	Selected            int             `json:"selected,omitempty"`
	Total               int             `json:"total,omitempty"`
	TruncationBanner    string          `json:"truncation_banner,omitempty"`
	BinarySkipped       int             `json:"binary_skipped,omitempty"`
	PrefilterSkipped    int             `json:"prefilter_skipped,omitempty"`
	FilesBytesTruncated int             `json:"files_bytes_truncated,omitempty"`
	// FilesParseFailed counts unavailable parses.
	FilesParseFailed       int                   `json:"files_parse_failed,omitempty"`
	ParseFailures          []tsparse.FileFailure `json:"parse_failures,omitempty"`
	ParseFailuresTruncated bool                  `json:"parse_failures_truncated,omitempty"`
	FilesUnreadable        int                   `json:"files_unreadable,omitempty"`
	FilesSearched          int                   `json:"files_searched"`
}

type grepDirCount struct {
	Dir   string `json:"dir"`
	Count int    `json:"count"`
}

type grepSearch struct {
	ctx      context.Context
	boundary *sandbox.Boundary
	tctx     tools.ToolContext
	// reads resolves and opens every file the search reads.
	reads        *projectpaths.ReadSession
	pattern      string
	re           *regexp.Regexp
	structural   bool
	lang         string
	offset       int
	skipped      int
	maxMatches   int
	contextLines int
	pathFilter   sandbox.EntryGlob
	resp         *grepResponse
	matchSink    func(grepMatch)
	// require is what a file must contain to hold any match.
	require             litprefilter.Requirement
	stats               *grepEngineStats
	binarySkipped       int
	prefilterSkipped    int
	filesBytesTruncated int
	structuralParsed    int
	// filesParseFailed counts unavailable parses.
	filesParseFailed int
	parseFailures    []tsparse.FileFailure
	textScanned      int
	unreadable       int
	// subtreeFiles counts searched files per top-level directory.
	subtreeFiles map[string]int
	// drafts overlays unsaved editor text on disk content.
	drafts sourceview.DraftOverlay
}

type grepTarget struct {
	root        projectroot.RootRef
	fullRoot    string
	displayRoot string
}

type grepOptions struct {
	pattern         string
	caseInsensitive bool
	includeHidden   bool
	includeIgnored  bool
	structural      bool
	lang            string
	maxMatches      int
	maxMatchesArg   toolkit.BoundedInt
	contextLines    int
	pathGlob        string
}

func parseGrepArgs(args map[string]any) (grepOptions, error) {
	pattern, _ := args["pattern"].(string)
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return grepOptions{}, toolkit.MissingArg("pattern")
	}
	if len(pattern) > safecmd.GrepMaxPatternLen {
		return grepOptions{}, safecmd.Reject("GREP_MATCH_BUDGET", map[string]any{
			"pattern_len": len(pattern),
			"max_len":     safecmd.GrepMaxPatternLen,
			"detail":      "pattern exceeds maximum length",
		})
	}
	pathGlob := ""
	if raw, ok := args["path_glob"].(string); ok {
		pathGlob = strings.TrimSpace(raw)
	}
	structural := toolkit.BoolArg(args, "structural", false)
	if _, explicit := args["structural"]; !explicit && grepMetavarRe.MatchString(pattern) {
		// Metavariables select structural matching unless explicitly disabled.
		structural = true
	}
	lang := ""
	if raw, ok := args["lang"].(string); ok {
		lang = strings.TrimSpace(raw)
	}
	maxMatchesArg := toolkit.BoundedIntArg(args, "max_matches", safecmd.GrepMaxMatches, 1, safecmd.GrepMaxMatches)
	return grepOptions{
		pattern:         pattern,
		caseInsensitive: toolkit.BoolArg(args, "case_insensitive", false),
		includeHidden:   toolkit.BoolArg(args, "include_hidden", true),
		includeIgnored:  toolkit.BoolArg(args, "include_ignored", true),
		structural:      structural,
		lang:            lang,
		maxMatches:      maxMatchesArg.Effective,
		maxMatchesArg:   maxMatchesArg,
		contextLines:    toolkit.ClampIntArg(args, "context_lines", 0, 0, safecmd.GrepMaxContextLines),
		pathGlob:        pathGlob,
	}, nil
}

func (t *GrepTool) resolveGrepTargets(ctx context.Context, tctx tools.ToolContext, args map[string]any) (relRoot string, targets []grepTarget, err error) {
	relRoot = "."
	if raw, ok := args["path"].(string); ok && strings.TrimSpace(raw) != "" {
		relRoot = strings.TrimSpace(raw)
	}
	if err := safecmd.RejectPathEscape(relRoot); err != nil {
		return "", nil, err
	}
	roots, err := projectpaths.UnionDiscoveryRoots(ctx, tctx, relRoot)
	if err != nil {
		return "", nil, err
	}
	if projectroot.IsUnionDiscoveryPath(relRoot) {
		targets = make([]grepTarget, 0, len(roots))
		for _, root := range roots {
			targets = append(targets, grepTarget{
				root:        root,
				fullRoot:    root.Path,
				displayRoot: projectpaths.QualifyAbs(tctx, root, root.Path),
			})
		}
		return relRoot, targets, nil
	}
	resolved, err := safecmd.ResolvePath(ctx, t.Boundary, tctx, relRoot)
	if err != nil {
		return "", nil, err
	}
	return relRoot, []grepTarget{{
		root:        resolved.Root,
		fullRoot:    resolved.Abs,
		displayRoot: resolved.DisplayPath,
	}}, nil
}

// scanFile matches one loaded text file.
func (s *grepSearch) scanFile(ctx context.Context, relSlash string, ident grepFileIdentity, content []byte, bytesTruncated bool) error {
	if !s.pathFilter.Match(relSlash) {
		return nil
	}
	if s.structural {
		s.textScanned++
		s.noteSubtree(relSlash)
		if bytesTruncated {
			s.filesBytesTruncated++
		}
		return s.scanStructural(ctx, relSlash, ident, content)
	}
	if s.offset > 0 {
		s.noteSubtree(relSlash)
		return s.scanPaginatedText(relSlash, ident, content, bytesTruncated)
	}
	out := s.scanContent(relSlash, ident, content)
	out.bytesTruncated = bytesTruncated
	return s.applyFileOutcome(out)
}

// noteSubtree attributes a searched file to its top-level directory.
func (s *grepSearch) noteSubtree(rel string) {
	top := "."
	if i := strings.IndexByte(rel, '/'); i > 0 {
		top = rel[:i]
	}
	if s.subtreeFiles == nil {
		s.subtreeFiles = map[string]int{}
	}
	s.subtreeFiles[top]++
}

// scanContent matches non-structural text.
func (s *grepSearch) scanContent(relSlash string, ident grepFileIdentity, content []byte) grepFileOutcome {
	out := grepFileOutcome{rel: relSlash}
	if !s.pathFilter.Match(relSlash) {
		out.globSkipped = true
		return out
	}
	if !s.require.Empty() {
		if s.stats != nil {
			s.stats.PrefilterRan.Add(1)
		}
		if !s.require.Matches(content) {
			if s.stats != nil {
				s.stats.PrefilterSkipped.Add(1)
			}
			out.prefilterSkipped = true
			return out
		}
	}
	if s.stats != nil {
		s.stats.RegexOrScanRuns.Add(1)
	}
	if s.matchSink != nil {
		out.err = s.forEachTextMatch(relSlash, ident, content, func(match grepMatch) bool {
			s.matchSink(match)
			return true
		})
		return out
	}
	matches, err := s.collectMatches(relSlash, ident, content)
	out.matches = matches
	out.err = err
	return out
}

// scanStructural matches supported syntax trees and rejects invalid patterns.
func (s *grepSearch) scanStructural(ctx context.Context, relSlash string, ident grepFileIdentity, content []byte) error {
	if _, ok := structrewrite.SupportedLanguage(s.lang, relSlash); !ok {
		return nil
	}
	s.structuralParsed++
	res, err := structrewrite.Run(ctx, structrewrite.Request{
		LangName: s.lang,
		Filename: relSlash,
		Source:   content,
		Pattern:  s.pattern,
	})
	if err != nil {
		var langErr *structrewrite.ErrLanguageUnknown
		if errors.As(err, &langErr) {
			return sourceview.LanguageUnknown(relSlash, s.lang)
		}
		// Failed analysis contributes diagnostics, not a no-match result.
		var failure *tsparse.Failure
		if errors.As(err, &failure) {
			var pattern *structrewrite.PatternError
			if errors.As(err, &pattern) {
				return sourceview.ParseReject(relSlash, "pattern", err)
			}
			s.structuralParsed--
			s.filesParseFailed++
			if len(s.parseFailures) < tsparse.MaxFailureExamples {
				s.parseFailures = append(s.parseFailures, tsparse.FileFailure{Path: relSlash, Phase: "source", Failure: failure})
			}
			return nil
		}
		var invalid *structrewrite.PatternError
		if errors.As(err, &invalid) {
			return sourceview.PatternInvalid(relSlash, err)
		}
		return fmt.Errorf("structural source parse %s: %w", relSlash, err)
	}
	for _, m := range res.Matches {
		if s.skipped < s.offset {
			s.skipped++
			continue
		}
		entry := grepMatch{Path: relSlash, Line: m.StartRow + 1, Content: m.Text, rootID: ident.rootID, scopeRel: ident.scopeRel}
		if len(m.Bindings) > 0 {
			entry.Bindings = m.Bindings
		}
		if s.matchSink != nil {
			s.matchSink(entry)
			continue
		}
		s.resp.Matches = append(s.resp.Matches, entry)
		if len(s.resp.Matches) >= s.maxMatches {
			s.resp.Truncated = true
			return errGrepMatchCap
		}
	}
	return nil
}
