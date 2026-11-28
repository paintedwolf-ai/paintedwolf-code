package evidence

import (
	"strings"

	"github.com/lycaon/lycaon/internal/sandbox"
)

// Verdict is the resolver outcome. Grounded reports which values accept the
// citation; the rest block the strict completion paths.
type Verdict string

const (
	VerdictMatched      Verdict = "matched"
	VerdictTraced       Verdict = "traced"
	VerdictUnverifiable Verdict = "unverifiable"
	// VerdictBound is a unique host recovery of a loose citation.
	VerdictBound Verdict = "bound"
	// VerdictAmbiguous matches multiple ledger records.
	VerdictAmbiguous Verdict = "ambiguous"
)

// Grounded reports whether the verdict accepts the citation.
func (v Verdict) Grounded() bool {
	switch v {
	case VerdictMatched, VerdictTraced, VerdictBound:
		return true
	default:
		return false
	}
}

// Triple is the semantic claim shape agents cite: path, line, excerpt.
type Triple struct {
	Path    string
	Line    int
	Excerpt string
}

// Resolution is host-minted provenance for one triple resolution.
type Resolution struct {
	Handle            string
	Path              string
	Line              int
	EndLine           int // closes a validated inclusive range; zero otherwise
	LineCorrectedFrom int
	Excerpt           string
	Verdict           Verdict
	Reason            string
}

// Resolve binds a citation triple against the path-indexed ledger.
func Resolve(
	roots CitationRoots,
	triple Triple,
	ev Ledger,
	handleHint string,
) Resolution {
	path := LedgerPathKey(roots, triple.Path, ev.ByPath)
	if path == "" {
		path = NormalizeResolvePath(roots.ProjectDir, triple.Path)
	}
	line := triple.Line
	excerpt := strings.TrimSpace(triple.Excerpt)
	out := Resolution{
		Path:    path,
		Line:    line,
		Excerpt: excerpt,
	}

	if path == "" || ev.ByPath == nil {
		out.Verdict = VerdictUnverifiable
		return out
	}
	handles := ev.ByPath[path]
	if len(handles) == 0 {
		out.Verdict = VerdictUnverifiable
		return out
	}

	matchedHandle, ambiguous := matchedHandleForPath(ev, path, line, excerpt, handleHint)
	if ambiguous {
		out.Verdict = VerdictAmbiguous
		return out
	}
	if matchedHandle != "" {
		out.Handle = matchedHandle
		out.Verdict = VerdictMatched
		// Only a verified excerpt is exact: without one the line was seen, not checked.
		if excerpt == "" {
			out.Verdict = VerdictTraced
		}
		if hint := strings.TrimSpace(handleHint); hint != "" && hint != matchedHandle {
			out.Verdict = VerdictBound
		}
		return out
	}

	if anchor, handle, ok := reanchorExcerpt(ev, path, excerpt); ok {
		out.LineCorrectedFrom, out.Line, out.Handle, out.Verdict = line, anchor, handle, VerdictMatched
		if hint := strings.TrimSpace(handleHint); hint != "" && hint != handle {
			out.Verdict = VerdictBound
		}
		return out
	}

	if excerpt != "" && excerptMatchesOtherPath(ev, path, line, excerpt) {
		out.Verdict = VerdictUnverifiable
		return out
	}

	out.Handle = bestHandleForPath(ev, path, line, handleHint)
	out.Verdict = VerdictTraced
	return out
}

// NormalizeResolvePath normalizes a citation path token for resolution display.
func NormalizeResolvePath(projectDir, raw string) string {
	if norm, ok := NormalizeCitationPath(projectDir, raw); ok {
		return norm
	}
	slash := NormalizeLedgerPath(raw)
	if slash != "" && !sandbox.HasParentTraversal(slash) {
		return slash
	}
	return ""
}

// recordCitedLineBytes extracts the delivered content at the cited line(s) for
// ambiguity comparison across current records (never prose), reusing the host's
// numbered-line grammar via lineContentIndex (hostmarker.ParseNumberedLine).
func recordCitedLineBytes(rec Record, claimPath string, line int) (string, bool) {
	if line <= 0 {
		if len(rec.Body) == 0 {
			return "", false
		}
		return strings.Join(rec.Body, "\n"), true
	}
	if idx := lineContentIndex(rec, claimPath); idx != nil {
		if text, ok := idx[line]; ok {
			return text, true
		}
	}
	if len(rec.LineRanges) == 1 {
		idx := line - rec.LineRanges[0].Start
		if idx >= 0 && idx < len(rec.Body) {
			return rec.Body[idx], true
		}
	}
	return "", false
}

func matchedHandleForPath(ev Ledger, path string, line int, excerpt string, handleHint string) (string, bool) {
	if handleHint != "" && ExcerptMatchesHandle(ev, handleHint, path, line, excerpt) {
		return handleHint, false
	}
	var matches []string
	for _, handle := range ev.ByPath[path] {
		if ExcerptMatchesHandle(ev, handle, path, line, excerpt) {
			matches = append(matches, handle)
		}
	}
	if len(matches) == 0 {
		return "", false
	}
	if len(matches) == 1 {
		return matches[0], false
	}
	// Multiple current records match every supplied field. Compare their delivered
	// bytes at the cited coordinate: differing bytes are VerdictAmbiguous;
	// identical delivered bytes stay grounded on the newest matching record for
	// determinism; when fewer than two candidates carry delivered bytes at the
	// coordinate (e.g. mutations or surveys), preserve existing ByPath order.
	var refBytes string
	refCount := 0
	var newestWithBytes string
	for _, h := range matches {
		rec, ok := ev.Handles[h]
		if !ok {
			continue
		}
		b, ok := recordCitedLineBytes(rec, path, line)
		if !ok {
			continue
		}
		if refCount == 0 {
			refBytes = b
		} else if b != refBytes {
			return "", true
		}
		refCount++
		newestWithBytes = h
	}
	if refCount >= 2 {
		return newestWithBytes, false
	}
	return matches[0], false
}

func excerptMatchesOtherPath(ev Ledger, citedPath string, line int, excerpt string) bool {
	excerpt = strings.TrimSpace(excerpt)
	if excerpt == "" || ev.ByPath == nil {
		return false
	}
	for path, handles := range ev.ByPath {
		if path == citedPath {
			continue
		}
		for _, handle := range handles {
			if ExcerptMatchesHandle(ev, handle, path, line, excerpt) {
				return true
			}
		}
	}
	return false
}

func bestHandleForPath(ev Ledger, path string, line int, handleHint string) string {
	if hint := validHandleHintForPath(ev, path, handleHint); hint != "" {
		return hint
	}
	handles := ev.ByPath[path]
	for i := len(handles) - 1; i >= 0; i-- {
		handle := handles[i]
		rec, ok := ev.Handles[handle]
		if !ok {
			continue
		}
		if line > 0 && len(rec.LineRanges) > 0 && !LineInRanges(line, rec.LineRanges) {
			continue
		}
		return handle
	}
	if handle, ok := HandleForPath(ev, path); ok {
		return handle
	}
	return ""
}

func validHandleHintForPath(ev Ledger, path, handleHint string) string {
	handleHint = strings.TrimSpace(handleHint)
	if handleHint == "" {
		return ""
	}
	if _, ok := ResolveHandle(ev, handleHint); !ok {
		return ""
	}
	for _, handle := range ev.ByPath[path] {
		if handle == handleHint {
			return handleHint
		}
	}
	return ""
}
