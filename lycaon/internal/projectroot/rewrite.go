package projectroot

import (
	"path/filepath"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/fspath"
)

// RewriteAttachedRootPaths replaces absolute paths under any attached root with
// their canonical display spelling, keyed on the session's active root. Tokens
// that merely contain a root path as a substring are left alone.
func RewriteAttachedRootPaths(roots []RootRef, activeRootID, s string) string {
	if s == "" || len(roots) == 0 || !strings.Contains(s, "/") {
		return s
	}
	prefixes := rewritePrefixes(roots)
	if len(prefixes) == 0 {
		return s
	}
	type rewriteSpan struct{ start, end int; replacement string }
	var spans []rewriteSpan
	for _, prefix := range prefixes {
		from := 0
		for {
			idx := strings.Index(s[from:], prefix)
			if idx < 0 {
				break
			}
			idx += from
			from = idx + len(prefix)
			tokenStart, tokenEnd, ok := rewriteTokenSpan(s, idx, len(prefix))
			if !ok {
				continue
			}
			token := s[tokenStart:tokenEnd]
			root, rootOK := rewriteTokenRoot(roots, token)
			if !rootOK {
				continue
			}
			spans = append(spans, rewriteSpan{tokenStart, tokenEnd, Qualify(roots, activeRootID, root, token)})
		}
	}
	if len(spans) == 0 {
		return s
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].start < spans[j].start })
	out := s
	cursor := len(s)
	for i := len(spans) - 1; i >= 0; i-- {
		sp := spans[i]
		if sp.end > cursor {
			continue
		}
		out = out[:sp.start] + sp.replacement + out[sp.end:]
		cursor = sp.start
	}
	return out
}

// rewritePrefixes lists the absolute spellings a root may appear as, longest
// first so a nested root wins over its parent.
func rewritePrefixes(roots []RootRef) []string {
	seen := map[string]bool{}
	var prefixes []string
	for _, root := range roots {
		for _, spelling := range rootPathSpellings(root) {
			if seen[spelling] {
				continue
			}
			seen[spelling] = true
			prefixes = append(prefixes, spelling)
		}
	}
	sort.Slice(prefixes, func(i, j int) bool { return len(prefixes[i]) > len(prefixes[j]) })
	return prefixes
}

// rootPathSpellings lists the slash-form absolute spellings a root's path may
// appear as: its configured path and its canonical (case-resolved) form.
func rootPathSpellings(root RootRef) []string {
	rootAbs := strings.TrimSpace(root.Path)
	if rootAbs == "" || !filepath.IsAbs(rootAbs) {
		return nil
	}
	var spellings []string
	seen := map[string]bool{}
	for _, candidate := range []string{filepath.Clean(rootAbs), filepath.Clean(filepath.FromSlash(fspath.CanonicalPath(rootAbs)))} {
		slash := filepath.ToSlash(candidate)
		if slash == "" || slash == "/" || seen[slash] {
			continue
		}
		seen[slash] = true
		spellings = append(spellings, slash)
	}
	return spellings
}

// rewriteFirstToken finds the next absolute attached-root path in s and returns
// its token span with the display replacement.
func rewriteFirstToken(s string, prefixes []string, roots []RootRef, activeRootID string) (start, end int, replacement string, found bool) {
	best := -1
	for _, prefix := range prefixes {
		from := 0
		for {
			idx := strings.Index(s[from:], prefix)
			if idx < 0 {
				break
			}
			idx += from
			if tokenStart, tokenEnd, ok := rewriteTokenSpan(s, idx, len(prefix)); ok {
				if best < 0 || idx < best {
					root, rootOK := rewriteTokenRoot(roots, s[tokenStart:tokenEnd])
					if !rootOK {
						continue
					}
					best = idx
					start, end, found = tokenStart, tokenEnd, true
					replacement = Qualify(roots, activeRootID, root, s[tokenStart:tokenEnd])
				}
			}
			from = idx + len(prefix)
		}
	}
	return start, end, replacement, found
}

// rewriteTokenSpan reports whether the prefix at idx starts a standalone path
// token, and returns the token span covering the optional relative suffix.
func rewriteTokenSpan(s string, idx, prefixLen int) (start, end int, ok bool) {
	start = idx
	for start > 0 && !isRewriteTokenBoundary(s[start-1]) {
		start--
	}
	if start != idx {
		return 0, 0, false
	}
	end = idx + prefixLen
	if end < len(s) && !isRewriteTokenBoundary(s[end]) {
		if s[end] != '/' {
			return 0, 0, false
		}
		for end < len(s) && !isRewriteTokenBoundary(s[end]) {
			end++
		}
	}
	return start, end, true
}

// rewriteTokenRoot finds the attached root that contains the token, matching
// the longest root path so a nested root wins over its parent.
func rewriteTokenRoot(roots []RootRef, token string) (RootRef, bool) {
	slash := filepath.ToSlash(token)
	var match RootRef
	matchLen := -1
	for _, root := range roots {
		for _, spelling := range rootPathSpellings(root) {
			if slash != spelling && !strings.HasPrefix(slash, spelling+"/") {
				continue
			}
			if matchLen < len(spelling) {
				match = root
				matchLen = len(spelling)
			}
		}
	}
	return match, matchLen >= 0
}

func isRewriteTokenBoundary(c byte) bool {
	switch c {
	case ' ', '\t', '\n', '\r', '"', '\'', '(', ')', '[', ']', '{', '}', '<', '>', '=', ',', ';':
		return true
	default:
		return false
	}
}
