package search

import (
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/textfile"
	"github.com/lycaon/lycaon/pkg/api"
)

const (
	ReplaceMaxFiles = 500
	ReplaceMaxHunks = 5000
	// replaceReadMaxBytes matches the project source read ceiling so preview
	// excludes the same oversize files the apply path cannot rewrite.
	replaceReadMaxBytes = 4 * 1024 * 1024
)

// ReplaceHunk is one match occurrence with the full text of the spanned lines
// before and after the replacement. Single-line matches have EndLine == Line.
type ReplaceHunk struct {
	Line    int
	EndLine int
	Before  string
	After   string
	Context api.SearchReplaceHunkContext
	// start/end are byte offsets of the match within the file content; frag is
	// the expanded replacement for exactly that span.
	start int
	end   int
	frag  string
}

// ReplaceFilePreview is one file's replace plan.
type ReplaceFilePreview struct {
	RootID string
	Path   string
	SHA256 string
	Hunks  []ReplaceHunk
}

// ReplacePreviewState separates preparation from reviewable, possibly limited results.
type ReplacePreviewState string

const (
	ReplacePreviewPreparing ReplacePreviewState = "preparing"
	ReplacePreviewReady     ReplacePreviewState = "ready"
	ReplacePreviewLimited   ReplacePreviewState = "limited"
)

type ReplacePreviewResult struct {
	State     ReplacePreviewState
	Issues    []Issue
	Files     []ReplaceFilePreview
	Truncated bool
}

// ReplacePreviewRequest drives PreviewReplace.
type ReplacePreviewRequest struct {
	Query       Node
	Replacement string
	Flags       MatchFlags
	Roots       []CodeRoot
	ExcludeDirs []string
}

// lineIndex maps content byte offsets to 1-based line numbers.
type lineIndex struct {
	starts []int
}

func newLineIndex(content string) lineIndex {
	starts := []int{0}
	for i := 0; i < len(content); i++ {
		if content[i] == '\n' {
			starts = append(starts, i+1)
		}
	}
	return lineIndex{starts: starts}
}

func (ix lineIndex) lineOf(off int) int {
	return sort.Search(len(ix.starts), func(i int) bool { return ix.starts[i] > off })
}

func (ix lineIndex) lineStart(line int) int {
	return ix.starts[line-1]
}

func (ix lineIndex) lineEnd(line, contentLen int) int {
	if line < len(ix.starts) {
		return ix.starts[line] - 1
	}
	return contentLen
}

func collectReplaceHunks(content string, matcher replaceMatcher, replacement string) []ReplaceHunk {
	spans := matcher.findAll(content, replacement)
	if len(spans) == 0 {
		return nil
	}
	ix := newLineIndex(content)
	out := make([]ReplaceHunk, 0, len(spans))
	for _, span := range spans {
		s, e := span.start, span.end
		if s < 0 || e > len(content) || s > e {
			continue
		}
		startLine := ix.lineOf(s)
		endOff := s
		if e > s {
			endOff = e - 1
		}
		endLine := ix.lineOf(endOff)
		// A consumed newline joins the following line into the preview.
		if e > s && content[e-1] == '\n' {
			endLine = ix.lineOf(e)
		}
		lineStart := ix.lineStart(startLine)
		lineEnd := ix.lineEnd(endLine, len(content))
		frag := span.fragment
		out = append(out, ReplaceHunk{
			Line:    startLine,
			EndLine: endLine,
			Before:  content[lineStart:lineEnd],
			After:   content[lineStart:s] + frag + content[e:lineEnd],
			Context: api.SearchReplaceContextCode,
			start:   s,
			end:     e,
			frag:    frag,
		})
	}
	return out
}

// ReplaceApplyFile is one file in an apply request.
type ReplaceApplyFile struct {
	RootID string
	Path   string
	SHA256 string
	// HunkIndexes are 0-based indices into the previewed hunk list for this file.
	HunkIndexes []int
}

// ReplaceContentRead is one file snapshot for planning.
type ReplaceContentRead struct {
	Content  string
	SHA256   string
	Encoding string
	Mode     fs.FileMode
	// Truncated marks content over the read limit; Binary marks non-text.
	// An empty file is neither — it simply has no matches.
	Truncated bool
	Binary    bool
}

// ReplaceContentStore reads guarded project source.
type ReplaceContentStore interface {
	ReadReplaceContent(rootID, path string) (ReplaceContentRead, error)
}

// Known skip reason sentinels returned by ReplaceContentStore adapters.
var (
	ErrReplaceWriteConflict = errors.New("source_write_conflict")
	ErrReplaceTooLarge      = errors.New("source_too_large")
	ErrReplaceBinary        = errors.New("source_binary")
	ErrReplaceNotFound      = errors.New("source_not_found")
	ErrReplacePathDenied    = errors.New("source_path_denied")
)

// ReplacePlanRequest defines one replace plan.
type ReplacePlanRequest struct {
	Query       Node
	Replacement string
	Flags       MatchFlags
	Store       ReplaceContentStore
	Files       []ReplaceApplyFile
}

// ReplacePlannedWrite is one fully validated after-image in a replace plan.
type ReplacePlannedWrite struct {
	OutcomeIndex int
	RootID       string
	Path         string
	Content      string
	Encoding     string
	BaseSHA256   string
	AfterSHA256  string
	Before       []byte
	After        []byte
}

// ReplacePlanResult separates planning from the filesystem commit.
type ReplacePlanResult struct {
	Files  []ReplaceFileOutcome
	Writes []ReplacePlannedWrite
}

// ReplaceFileOutcome is one file's apply result.
type ReplaceFileOutcome struct {
	RootID  string
	Path    string
	Applied bool
	Skipped bool
	Reason  string
	Matches int
}

// PlanReplace captures selected before/after images.
func PlanReplace(req ReplacePlanRequest) (ReplacePlanResult, error) {
	if req.Store == nil {
		return ReplacePlanResult{}, fmt.Errorf("replace store required")
	}
	pattern, err := replacementPattern(req.Query)
	if err != nil {
		return ReplacePlanResult{}, err
	}
	matcher, err := compileReplaceMatcher(pattern, req.Flags)
	if err != nil {
		return ReplacePlanResult{}, ensureMatchError(err)
	}
	paths, err := compilePathGlobs(req.Flags)
	if err != nil {
		return ReplacePlanResult{}, err
	}
	out := make([]ReplaceFileOutcome, 0, len(req.Files))
	writes := make([]ReplacePlannedWrite, 0, len(req.Files))
	for _, file := range req.Files {
		outcome, write := planOneReplaceFile(req, matcher, paths, file)
		if write != nil {
			write.OutcomeIndex = len(out)
			writes = append(writes, *write)
		}
		out = append(out, outcome)
	}
	return ReplacePlanResult{Files: out, Writes: writes}, nil
}

func planOneReplaceFile(
	req ReplacePlanRequest,
	matcher replaceMatcher,
	paths pathGlobFilter,
	file ReplaceApplyFile,
) (ReplaceFileOutcome, *ReplacePlannedWrite) {
	base := ReplaceFileOutcome{RootID: file.RootID, Path: file.Path}
	if !replacementPathAllowed(req.Query, file.Path) || !paths.allows(file.Path) {
		base.Skipped = true
		base.Reason = "source_path_denied"
		return base, nil
	}
	read, err := req.Store.ReadReplaceContent(file.RootID, file.Path)
	if err != nil {
		base.Skipped = true
		base.Reason = replaceSkipReason(err)
		return base, nil
	}
	if read.Binary {
		base.Skipped = true
		base.Reason = "source_binary"
		return base, nil
	}
	if read.Truncated || read.SHA256 == "" {
		base.Skipped = true
		base.Reason = "source_too_large"
		return base, nil
	}
	wantSHA := strings.ToLower(strings.TrimSpace(file.SHA256))
	if wantSHA == "" || !strings.EqualFold(read.SHA256, wantSHA) {
		base.Skipped = true
		base.Reason = "source_write_conflict"
		return base, nil
	}
	hunks := collectReplaceHunks(read.Content, matcher, req.Replacement)
	kept := selectHunks(hunks, file.HunkIndexes)
	if len(kept) == 0 {
		base.Skipped = true
		base.Reason = "no_hunks_selected"
		return base, nil
	}
	next, ok := applyHunksToContent(read.Content, kept)
	if !ok {
		base.Skipped = true
		base.Reason = "source_write_conflict"
		return base, nil
	}
	limits := textfile.LimitsForRaw(replaceReadMaxBytes)
	beforeRaw, err := textfile.EncodeBounded(read.Content, read.Encoding, limits)
	if err != nil {
		base.Skipped = true
		base.Reason = "source_binary"
		return base, nil
	}
	afterRaw, err := textfile.EncodeBounded(next, read.Encoding, limits)
	if err != nil {
		base.Skipped = true
		base.Reason = "source_binary"
		return base, nil
	}
	base.Applied = true
	base.Matches = len(kept)
	return base, &ReplacePlannedWrite{RootID: file.RootID, Path: file.Path, Content: next,
		Encoding: read.Encoding, BaseSHA256: read.SHA256, AfterSHA256: textfile.SHA256(afterRaw),
		Before: beforeRaw, After: afterRaw}
}

func selectHunks(hunks []ReplaceHunk, indexes []int) []ReplaceHunk {
	if len(indexes) == 0 {
		return nil
	}
	seen := map[int]bool{}
	var out []ReplaceHunk
	for _, idx := range indexes {
		if idx < 0 || idx >= len(hunks) || seen[idx] {
			continue
		}
		seen[idx] = true
		out = append(out, hunks[idx])
	}
	return out
}

// Right-to-left replacement preserves offsets; overlapping spans are rejected.
func applyHunksToContent(content string, kept []ReplaceHunk) (string, bool) {
	sorted := append([]ReplaceHunk(nil), kept...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].start > sorted[j].start })
	prevStart := len(content)
	for _, h := range sorted {
		if h.start < 0 || h.end > len(content) || h.start > h.end || h.end > prevStart {
			return "", false
		}
		content = content[:h.start] + h.frag + content[h.end:]
		prevStart = h.start
	}
	return content, true
}

func replaceSkipReason(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrReplaceWriteConflict):
		return "source_write_conflict"
	case errors.Is(err, ErrReplaceTooLarge):
		return "source_too_large"
	case errors.Is(err, ErrReplaceBinary):
		return "source_binary"
	case errors.Is(err, ErrReplaceNotFound):
		return "source_not_found"
	case errors.Is(err, ErrReplacePathDenied):
		return "source_path_denied"
	default:
		return "source_write_failed"
	}
}
