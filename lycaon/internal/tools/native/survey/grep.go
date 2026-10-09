package survey

import (
	"context"
	"errors"
	"fmt"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"regexp/syntax"
	"sort"
	"strings"
	"sync/atomic"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/litprefilter"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/structrewrite"
	"github.com/lycaon/lycaon/internal/textfile"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/sourceview"
	"github.com/lycaon/lycaon/internal/tools/native/toolkit"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
	"github.com/lycaon/lycaon/internal/tools/readcaps"
	"github.com/lycaon/lycaon/internal/tools/safecmd"
	"github.com/lycaon/lycaon/internal/toolscope"
	"github.com/lycaon/lycaon/internal/tsparse"
	"github.com/lycaon/lycaon/pkg/api"
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
	// Scope overrides process defaults.
	Scope *toolscope.Config
	// FileCount fills missing or stale repository counts.
	FileCount     func(projectDir string) (count int, ok bool)
	densenessGate grepDensenessGate
}

func (t *GrepTool) Name() string { return "grep" }

type grepMatch struct {
	Path          string            `json:"path"`
	Line          int               `json:"line"`
	Content       string            `json:"content"`
	Match         string            `json:"match,omitempty"`
	Count         int               `json:"-"`
	Bindings      map[string]string `json:"bindings,omitempty"`
	ContextBefore []string          `json:"context_before,omitempty"`
	ContextAfter  []string          `json:"context_after,omitempty"`
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
	// FilesFromEditor counts searches of unsaved editor text.
	FilesFromEditor int `json:"files_from_editor,omitempty"`
	// Inventory is set when the file list came from a generation that predates
	// the latest repository changes.
	Inventory *inventoryFacts `json:"inventory,omitempty"`
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
	// Drafts overlay disk content; fromEditor counts affected files.
	drafts     sourceview.DraftOverlay
	fromEditor atomic.Int64
}

type grepTarget struct {
	root        projectroot.RootRef
	fullRoot    string
	displayRoot string
}

func (t *GrepTool) Run(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	ctx, _ = withInventoryReport(ctx)
	opts, err := parseGrepArgs(args)
	if err != nil {
		return "", err
	}
	filter, err := compileSurveyGlob("path_glob", opts.pathGlob)
	if err != nil {
		return "", err
	}
	ctx, cancel := (safecmd.Caps{Timeout: safecmd.GrepTimeout}).WithTimeout(ctx)
	defer cancel()
	// Validate patterns before repository-density checks.
	re, err := compileGrepPattern(opts)
	if err != nil {
		return "", err
	}
	relRoot, targets, err := t.resolveGrepTargets(ctx, tctx, args)
	if err != nil {
		return "", grepExecutionError(nil, err)
	}
	reportGrepScope(tctx, targets)
	if opts.structural && strings.TrimSpace(opts.lang) != "" {
		if _, ok := structrewrite.SupportedLanguage(opts.lang, ""); !ok {
			return "", sourceview.LanguageUnknown(relRoot, opts.lang)
		}
	}
	if out, guarded, gerr := t.maybeGuardOpenRootScope(ctx, args, tctx, opts); guarded {
		return out, grepExecutionError(nil, gerr)
	}
	offset := toolkit.ClampIntArg(args, "offset", 0, 0, 1_000_000)
	stats := &grepEngineStats{}
	reads := projectpaths.NewReadSession(t.Boundary, tctx)
	defer reads.Close()
	search := &grepSearch{
		ctx: ctx, boundary: t.Boundary, tctx: tctx, reads: reads, pattern: opts.pattern, re: re,
		structural: opts.structural, lang: opts.lang,
		offset: offset, maxMatches: opts.maxMatches,
		contextLines: opts.contextLines, pathFilter: filter,
		resp:    &grepResponse{Matches: []grepMatch{}, Offset: offset},
		require: litprefilter.Extract(opts.pattern, opts.caseInsensitive),
		stats:   stats,
		drafts:  sourceview.DraftsFor(ctx, tctx),
	}
	search.publishProgress("selecting")
	for _, target := range targets {
		info, err := os.Stat(target.fullRoot)
		if err != nil {
			return "", grepRootStatErr(target.displayRoot, err)
		}
		if !info.IsDir() {
			out, err := t.grepSingleFile(ctx, relRoot, target.displayRoot, target.fullRoot, info, opts.includeHidden, search, opts, args)
			if err != nil {
				return "", grepExecutionError(search, err)
			}
			if search.resp.Truncated {
				return out, nil
			}
			continue
		}
		walkExtra := sandbox.SurveyOptions{}
		if !opts.includeIgnored {
			walkExtra.Scope = newIgnoreScope(target.root, target.fullRoot)
		}
		if err := t.grepWalkTree(ctx, tctx, target, opts.includeHidden, search, walkExtra); err != nil {
			return "", grepExecutionError(search, err)
		}
		if search.resp.Truncated {
			break
		}
	}
	if err := ctx.Err(); err != nil {
		return "", grepExecutionError(search, err)
	}
	return t.finalizeGrepResponse(ctx, relRoot, search, opts, args)
}

func (t *GrepTool) finalizeGrepResponse(ctx context.Context, root string, search *grepSearch, opts grepOptions, args map[string]any) (string, error) {
	resp := *search.resp
	resp.MaxMatches = opts.maxMatches
	resp.BinarySkipped = search.binarySkipped
	resp.PrefilterSkipped = search.prefilterSkipped
	if search.stats != nil {
		resp.PrefilterSkipped += int(search.stats.IndexBloomPruned.Load())
	}
	resp.FilesBytesTruncated = search.filesBytesTruncated
	resp.FilesParseFailed = search.filesParseFailed
	resp.ParseFailures = search.parseFailures
	resp.ParseFailuresTruncated = search.filesParseFailed > len(search.parseFailures)
	sourceview.ReportParseFailures(search.tctx, search.parseFailures, search.filesParseFailed)
	resp.FilesUnreadable = search.unreadable
	resp.FilesSearched = search.textScanned + resp.PrefilterSkipped
	resp.FilesFromEditor = int(search.fromEditor.Load())
	resp.Inventory = inventoryFactsFrom(ctx)
	search.publishProgress("searching")
	var bannerParts []string
	bannerParts = toolkit.AppendClampBanner(bannerParts, "max_matches", opts.maxMatchesArg)
	if resp.Truncated {
		next := search.offset + len(resp.Matches)
		resp.NextOffset = &next
		resp.Distribution = grepMatchDistribution(resp.Matches)
		bannerParts = append(bannerParts,
			fmt.Sprintf("showing %d matches from offset %d; use offset=%d, or scope path to a directory in 'distribution' to narrow", len(resp.Matches), resp.Offset, next),
		)
	}
	if resp.FilesBytesTruncated > 0 {
		bannerParts = append(bannerParts,
			fmt.Sprintf("%d file(s) exceeded the %d-byte grep cap — only the leading bytes were searched; use read with offset/limit on those paths (or narrow path) for content past the cap", resp.FilesBytesTruncated, hostGrepMaxFileBytes),
		)
	}
	if search.unreadable > 0 {
		bannerParts = append(bannerParts, fmt.Sprintf("%d file(s) could not be read; search coverage is incomplete", search.unreadable))
	}
	// A consumed offset suppresses empty-result guidance.
	if len(resp.Matches) == 0 && !resp.Truncated && search.skipped == 0 && search.unreadable == 0 && resp.FilesBytesTruncated == 0 && resp.FilesParseFailed == 0 {
		if opts.structural {
			resp.Note = emptyStructuralNote(ctx, search.structuralParsed)
		} else {
			resp.Note = emptyTextNote(ctx, search.textScanned+search.prefilterSkipped, opts.pattern)
		}
	}
	if len(bannerParts) > 0 {
		resp.TruncationBanner = toolkit.TruncationBanner(strings.Join(bannerParts, "; "))
	}
	unbounded := grepIsUnbounded(args, resp.Truncated)
	if unbounded {
		collected := append([]grepMatch(nil), resp.Matches...)
		resp = buildGrepZoomedOutResponse(ctx, resp, collected)
	}
	for _, match := range resp.Matches {
		search.tctx.RecordSourcePath(match.Path, api.NavigationEntryKindFile)
	}
	for _, hit := range resp.Highlights {
		search.tctx.RecordSourcePath(hit.Path, api.NavigationEntryKindFile)
	}
	recordGrepMatches(ctx, t.Boundary, search.tctx, search.re, search.structural, resp.Matches)
	return t.encodeGrepResponse(root, resp)
}

// grepMatchDistribution groups retained matches by path prefix.
func grepMatchDistribution(matches []grepMatch) []grepDirCount {
	counts := map[string]int{}
	for _, m := range matches {
		counts[grepPathBucket(m.Path)]++
	}
	out := make([]grepDirCount, 0, len(counts))
	for dir, n := range counts {
		out = append(out, grepDirCount{Dir: dir, Count: n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Dir < out[j].Dir
	})
	if len(out) > 8 {
		out = out[:8]
	}
	return out
}

func grepPathBucket(p string) string {
	p = strings.TrimPrefix(filepath.ToSlash(p), "./")
	segs := strings.SplitN(p, "/", 3)
	switch len(segs) {
	case 0, 1:
		return "."
	case 2:
		return segs[0]
	default:
		return segs[0] + "/" + segs[1]
	}
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

func compileGrepPattern(opts grepOptions) (*regexp.Regexp, error) {
	if opts.structural {
		return nil, nil
	}
	re, err := compileGrepRegex(opts.pattern, opts.caseInsensitive)
	if err != nil {
		var toolReject *toolrejection.ToolReject
		if errors.As(err, &toolReject) {
			return nil, toolReject
		}
		return nil, safecmd.Reject("GREP_REGEX_INVALID", map[string]any{
			"pattern": opts.pattern,
			"detail":  err.Error(),
		})
	}
	return re, nil
}

func (t *GrepTool) grepSingleFile(ctx context.Context, relRoot, displayRoot, fullRoot string, info fs.FileInfo, includeHidden bool, search *grepSearch, opts grepOptions, args map[string]any) (string, error) {
	relSlash := filepath.ToSlash(displayRoot)
	if (!includeHidden && isHiddenPath(relSlash)) || !search.pathFilter.Match(relSlash) {
		return t.finalizeGrepResponse(ctx, relRoot, search, opts, args)
	}
	content, binary, bytesTruncated, err := search.loadGrepFile(ctx, fullRoot, info)
	if err != nil {
		return "", err
	}
	var scanErr error
	if binary {
		scanErr = search.applyFileOutcome(grepFileOutcome{binarySkipped: true})
	} else {
		scanErr = search.scanFile(ctx, relSlash, content, bytesTruncated)
	}
	if scanErr != nil && !errors.Is(scanErr, errGrepMatchCap) {
		return "", scanErr
	}
	return t.finalizeGrepResponse(ctx, relRoot, search, opts, args)
}

// loadGrepFile serves one file as the person sees it: their unsaved editor
// text when they have it open, the file otherwise.
func (s *grepSearch) loadGrepFile(ctx context.Context, absPath string, info fs.FileInfo) (content []byte, binary, bytesTruncated bool, err error) {
	if text, ok := s.drafts.Lookup(absPath); ok {
		s.fromEditor.Add(1)
		if len(text) > hostGrepMaxFileBytes {
			return []byte(textfile.TrimIncompleteTail(text[:hostGrepMaxFileBytes])), false, true, nil
		}
		return text, false, false, nil
	}
	return readGrepFile(ctx, s.reads, absPath, info)
}

// scanFile matches one loaded text file.
func (s *grepSearch) scanFile(ctx context.Context, relSlash string, content []byte, bytesTruncated bool) error {
	if !s.pathFilter.Match(relSlash) {
		return nil
	}
	if s.structural {
		s.textScanned++
		s.noteSubtree(relSlash)
		if bytesTruncated {
			s.filesBytesTruncated++
		}
		return s.scanStructural(ctx, relSlash, content)
	}
	if s.offset > 0 {
		s.noteSubtree(relSlash)
		return s.scanPaginatedText(relSlash, content, bytesTruncated)
	}
	out := s.scanContent(relSlash, content)
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

// largestSubtree names the top-level directory holding the most searched files.
func (s *grepSearch) largestSubtree() (string, int) {
	best, count := "", 0
	for dir, n := range s.subtreeFiles {
		if n > count || n == count && dir < best {
			best, count = dir, n
		}
	}
	return best, count
}

// scanContent matches non-structural text.
func (s *grepSearch) scanContent(relSlash string, content []byte) grepFileOutcome {
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
		out.err = s.forEachTextMatch(relSlash, content, func(match grepMatch) bool {
			s.matchSink(match)
			return true
		})
		return out
	}
	matches, err := s.collectMatches(relSlash, content)
	out.matches = matches
	out.err = err
	return out
}

// scanStructural matches supported syntax trees and rejects invalid patterns.
func (s *grepSearch) scanStructural(ctx context.Context, relSlash string, content []byte) error {
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
		entry := grepMatch{Path: relSlash, Line: m.StartRow + 1, Content: m.Text}
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

func emptyStructuralNote(ctx context.Context, filesParsed int) string {
	out, err := guidance.RenderCatalog(ctx, guidance.SurveyGrepEmptyStructuralRef, map[string]any{
		"files_parsed": filesParsed,
	})
	if err != nil {
		return ""
	}
	return out
}

func emptyTextNote(ctx context.Context, filesSearched int, pattern string) string {
	out, err := guidance.RenderCatalog(ctx, guidance.SurveyGrepEmptyTextRef, map[string]any{
		"files_searched": filesSearched,
		"operators":      unescapedRegexOperators(pattern),
	})
	if err != nil {
		return ""
	}
	return out
}

// unescapedRegexOperators lists unescaped RE2 operators in first-seen order, backtick-quoted.
func unescapedRegexOperators(pattern string) string {
	const operators = ".*+?()[]{}|^$"
	var found []string
	seen := map[rune]bool{}
	escaped := false
	for _, r := range pattern {
		if escaped {
			escaped = false
			continue
		}
		if r == '\\' {
			escaped = true
			continue
		}
		if strings.ContainsRune(operators, r) && !seen[r] {
			seen[r] = true
			found = append(found, "`"+string(r)+"`")
		}
	}
	return strings.Join(found, ", ")
}

func (t *GrepTool) encodeGrepResponse(root string, resp grepResponse) (string, error) {
	pathsTouched := len(resp.Matches)
	if pathsTouched == 0 && len(resp.Highlights) > 0 {
		pathsTouched = len(resp.Highlights)
	}
	return safecmd.Shape(safecmd.ShapeInput{
		Tool:         "grep",
		Path:         root,
		PathsTouched: pathsTouched,
		Truncated:    resp.Truncated,
		Banner:       resp.TruncationBanner,
		Selected:     resp.Selected,
		Total:        resp.Total,
		Value:        resp,
	})
}

func compileGrepRegex(pattern string, caseInsensitive bool) (*regexp.Regexp, error) {
	flags := syntax.Perl
	if caseInsensitive {
		flags |= syntax.FoldCase
	}
	parsed, err := syntax.Parse(pattern, flags)
	if err != nil {
		return nil, err
	}
	if regexHasNestedRepeat(parsed) {
		return nil, safecmd.Reject("GREP_MATCH_BUDGET", map[string]any{
			"pattern":            pattern,
			"detail":             "nested quantifiers exceed safe budget",
			"grep_nested_repeat": true,
			"max_len":            safecmd.GrepMaxPatternLen,
		})
	}
	compiled, err := regexp.Compile(parsed.String())
	if err != nil {
		return nil, err
	}
	return compiled, nil
}

func regexHasNestedRepeat(re *syntax.Regexp) bool {
	return regexNestedRepeat(re, false)
}

func regexNestedRepeat(re *syntax.Regexp, insideHeavyRepeat bool) bool {
	if re == nil {
		return false
	}
	heavy := isHeavyRepeatOp(re.Op)
	if insideHeavyRepeat && heavy {
		return true
	}
	nextInside := insideHeavyRepeat || heavy
	for _, sub := range re.Sub {
		if regexNestedRepeat(sub, nextInside) {
			return true
		}
	}
	return false
}

func isHeavyRepeatOp(op syntax.Op) bool {
	switch op {
	case syntax.OpStar, syntax.OpPlus, syntax.OpRepeat:
		return true
	default:
		return false
	}
}

func lineMatches(line string, re *regexp.Regexp) (bool, string) {
	loc := re.FindStringIndex(line)
	if loc == nil {
		return false, ""
	}
	return true, line[loc[0]:loc[1]]
}

func isHiddenPath(relSlash string) bool {
	base := filepath.Base(relSlash)
	return strings.HasPrefix(base, ".")
}
