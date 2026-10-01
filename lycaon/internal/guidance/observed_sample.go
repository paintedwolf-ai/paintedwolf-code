package guidance

import (
	"path/filepath"
	"strings"
	"unicode"

	"github.com/lycaon/lycaon/internal/evidence"
)

// ObservedPathSample is one host-bound observed path for autobind closeouts.
type ObservedPathSample struct {
	Path string
	Line int
}

// SelectObservedPathSample prefers openable observations mentioned in the narrative.
func SelectObservedPathSample(ev evidence.Ledger, roots evidence.CitationRoots, narrative string, alreadyCited map[string]struct{}) []ObservedPathSample {
	handleable := handleableObservedPaths(ev, roots)
	if len(handleable) == 0 {
		return nil
	}

	preferred := preferredObservedPaths(ev, narrative, handleable)
	out := make([]ObservedPathSample, 0, ObservedSampleCap)
	seen := map[string]struct{}{}
	for path := range alreadyCited {
		path = filepath.ToSlash(strings.TrimSpace(path))
		if path != "" {
			seen[path] = struct{}{}
		}
	}
	appendSample := func(path string, line int) bool {
		if len(out) >= ObservedSampleCap {
			return false
		}
		path = filepath.ToSlash(strings.TrimSpace(path))
		if path == "" {
			return true
		}
		if _, dup := seen[path]; dup {
			return true
		}
		seen[path] = struct{}{}
		if line < 0 {
			line = 0
		}
		out = append(out, ObservedPathSample{Path: path, Line: line})
		return len(out) < ObservedSampleCap
	}

	for _, sample := range preferred {
		if !appendSample(sample.Path, sample.Line) {
			return out
		}
	}
	for _, path := range handleable {
		if !appendSample(path, 0) {
			return out
		}
	}
	return out
}

// SelectObservedURLSample prefers observed URLs mentioned in the narrative.
func SelectObservedURLSample(ev evidence.Ledger, narrative string, alreadyCited map[string]struct{}) []string {
	urls := evidence.ObservedURLsSorted(ev)
	if len(urls) == 0 {
		return nil
	}
	out := make([]string, 0, ObservedSampleCap)
	seen := map[string]struct{}{}
	for u := range alreadyCited {
		u = strings.TrimSpace(u)
		if u != "" {
			seen[u] = struct{}{}
		}
	}
	appendURL := func(u string) bool {
		if len(out) >= ObservedSampleCap {
			return false
		}
		u = strings.TrimSpace(u)
		if u == "" {
			return true
		}
		if _, dup := seen[u]; dup {
			return true
		}
		seen[u] = struct{}{}
		out = append(out, u)
		return len(out) < ObservedSampleCap
	}

	for _, u := range preferredObservedURLs(ev, narrative) {
		if !appendURL(u) {
			return out
		}
	}
	for _, u := range urls {
		if !appendURL(u) {
			return out
		}
	}
	return out
}

func handleableObservedPaths(ev evidence.Ledger, roots evidence.CitationRoots) []string {
	var out []string
	for _, path := range evidence.ObservedPathsSorted(ev) {
		if !observedPathEligibleForProse(path) {
			continue
		}
		if _, ok := evidence.HandleForPath(ev, path); !ok {
			continue
		}
		if openable := evidence.IsOpenablePath(roots, path); openable != nil && !*openable {
			continue
		}
		out = append(out, path)
	}
	return out
}

func preferredObservedURLs(ev evidence.Ledger, narrative string) []string {
	narrative = strings.TrimSpace(narrative)
	if narrative == "" {
		return nil
	}
	fileNorm := evidence.FileRegionNormalizer{}
	var out []string
	seen := map[string]struct{}{}
	for _, cite := range evidence.ObservedCitations(ev) {
		if cite.Kind != evidence.ObservedCitationKindURL {
			continue
		}
		if !narrativeContainsObserved(narrative, cite, fileNorm) {
			continue
		}
		u := strings.TrimSpace(cite.URL)
		if u == "" {
			u = strings.TrimSpace(cite.Token)
		}
		if u == "" {
			continue
		}
		if _, dup := seen[u]; dup {
			continue
		}
		seen[u] = struct{}{}
		out = append(out, u)
	}
	return out
}

func preferredObservedPaths(ev evidence.Ledger, narrative string, handleable []string) []ObservedPathSample {
	narrative = strings.TrimSpace(narrative)
	if narrative == "" || len(handleable) == 0 {
		return nil
	}

	handleableSet := map[string]struct{}{}
	for _, path := range handleable {
		handleableSet[path] = struct{}{}
	}

	fileNorm := evidence.FileRegionNormalizer{}
	preferred := map[string]int{}
	var order []string
	remember := func(path string, line int) {
		path = fileNorm.Normalize(path)
		if path == "" {
			return
		}
		if _, ok := handleableSet[path]; !ok {
			return
		}
		if prev, seen := preferred[path]; seen {
			if prev <= 0 && line > 0 {
				preferred[path] = line
			}
			return
		}
		preferred[path] = line
		order = append(order, path)
	}

	for _, cite := range evidence.ObservedCitations(ev) {
		switch cite.Kind {
		case evidence.ObservedCitationKindURL:
			continue
		case evidence.ObservedCitationKindPathLine, evidence.ObservedCitationKindBacktickPath:
			if !narrativeContainsObserved(narrative, cite, fileNorm) {
				continue
			}
			remember(cite.Path, cite.Line)
		}
	}

	// Ambiguous suffixes cannot select an observation.
	for _, path := range handleable {
		if _, seen := preferred[path]; seen {
			continue
		}
		if line, ok := uniqueObservedPathSuffixMention(narrative, path, handleable); ok {
			remember(path, line)
		}
	}

	out := make([]ObservedPathSample, 0, len(order))
	for _, path := range order {
		out = append(out, ObservedPathSample{Path: path, Line: preferred[path]})
	}
	return out
}

// uniqueObservedPathSuffixMention matches a suffix shared by exactly one citable path.
func uniqueObservedPathSuffixMention(narrative, path string, handleable []string) (line int, ok bool) {
	parts := strings.Split(filepath.ToSlash(path), "/")
	if len(parts) < 2 {
		return 0, false
	}
	for start := 1; start < len(parts); start++ {
		suffix := strings.Join(parts[start:], "/")
		if !observedPathEligibleForProse(suffix) {
			continue
		}
		line, mentioned := pathMentionInNarrative(narrative, suffix)
		if !mentioned {
			continue
		}
		matches := 0
		for _, candidate := range handleable {
			if candidate == suffix || strings.HasSuffix(candidate, "/"+suffix) {
				matches++
				if matches > 1 {
					break
				}
			}
		}
		if matches == 1 {
			return line, true
		}
	}
	return 0, false
}

func pathMentionInNarrative(narrative, path string) (line int, ok bool) {
	if line, ok := firstPathLineMention(narrative, path); ok {
		return line, true
	}
	if narrativeContainsPathToken(narrative, path) {
		return 0, true
	}
	return 0, false
}

func firstPathLineMention(narrative, path string) (line int, ok bool) {
	needle := path + ":"
	for idx := 0; idx < len(narrative); {
		i := strings.Index(narrative[idx:], needle)
		if i < 0 {
			return 0, false
		}
		i += idx
		if i > 0 && isPathTokenChar(rune(narrative[i-1])) {
			idx = i + 1
			continue
		}
		rest := narrative[i+len(needle):]
		digitEnd := 0
		for digitEnd < len(rest) && unicode.IsDigit(rune(rest[digitEnd])) {
			digitEnd++
		}
		if digitEnd == 0 {
			idx = i + 1
			continue
		}
		// Ranges bind to their start line.
		after := digitEnd
		if after < len(rest) && rest[after] == '-' {
			j := after + 1
			for j < len(rest) && unicode.IsDigit(rune(rest[j])) {
				j++
			}
			if j > after+1 {
				after = j
			}
		}
		if after < len(rest) && isPathTokenChar(rune(rest[after])) {
			idx = i + 1
			continue
		}
		if _, ln, splitOK := evidence.SplitPathLineToken(path + ":" + rest[:digitEnd]); splitOK {
			return ln, true
		}
		idx = i + 1
	}
	return 0, false
}
