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
	Handle  string
	Path    string
	Line    int
	Excerpt string
	Verdict Verdict
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

	if matchedHandle := matchedHandleForPath(ev, path, line, excerpt); matchedHandle != "" {
		out.Handle = matchedHandle
		out.Verdict = VerdictMatched
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

func matchedHandleForPath(ev Ledger, path string, line int, excerpt string) string {
	for _, handle := range ev.ByPath[path] {
		if ExcerptMatchesHandle(ev, handle, line, excerpt) {
			return handle
		}
	}
	return ""
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
			if ExcerptMatchesHandle(ev, handle, line, excerpt) {
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
