package guidance

import (
	"fmt"
	"github.com/lycaon/lycaon/internal/evidence"
	"path/filepath"
	"strings"
)

const (
	leakKindPathLine = evidence.ObservedCitationKindPathLine
	leakKindURL      = evidence.ObservedCitationKindURL
)

// TypedChannel is the report's typed citation channel for prose leak membership tests.
type TypedChannel struct {
	Paths     map[string]struct{}
	PathLines map[string]struct{}
	URLs      map[string]struct{}
}

// Leak is one observed citation token found in narrative prose.
type Leak struct {
	Token       string
	Kind        string
	AlsoInTyped bool
}

// WorkerNarrativeInput is narrative-only worker report text scanned for citation leaks.
type WorkerNarrativeInput struct {
	Brief         string
	ObjectivesMet []string
	RemainingRisk []string
	FindingNotes  []string
}

// ProseLeakEval classifies observed citations found in narrative fields.
type ProseLeakEval struct {
	Leaks              []Leak
	AdvisoryLeakTokens []string
	DuplicateTokens    []string
}

// ProseLeaks reports observed ledger citations present in narrative outside the typed channel.
func ProseLeaks(narrative string, ev evidence.Ledger, typed TypedChannel) []Leak {
	narrative = strings.TrimSpace(narrative)
	if narrative == "" {
		return nil
	}
	fileNorm := evidence.FileRegionNormalizer{}
	var out []Leak
	seen := map[string]struct{}{}
	for _, cite := range evidence.ObservedCitations(ev) {
		if !narrativeContainsObserved(narrative, cite, fileNorm) {
			continue
		}
		inTyped := observedInTypedChannel(typed, cite, fileNorm)
		key := cite.Kind + "|" + cite.Token
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, Leak{
			Token:       cite.Token,
			Kind:        cite.Kind,
			AlsoInTyped: inTyped,
		})
	}
	return out
}

func narrativeContainsObserved(narrative string, cite evidence.ObservedCitation, norm evidence.FileRegionNormalizer) bool {
	for _, variant := range narrativeVariants(cite, norm) {
		if variant == "" {
			continue
		}
		switch cite.Kind {
		case leakKindURL, leakKindPathLine:
			if strings.Contains(narrative, variant) {
				return true
			}
		default:
			if narrativeContainsPathToken(narrative, variant) {
				return true
			}
		}
	}
	return false
}

func observedPathEligibleForProse(path string) bool {
	path = strings.TrimSpace(path)
	if path == "" || path == "." {
		return false
	}
	if len(path) < 3 {
		return false
	}
	return true
}

func narrativeContainsPathToken(narrative, path string) bool {
	if !observedPathEligibleForProse(path) {
		return false
	}
	idx := strings.Index(narrative, path)
	if idx < 0 {
		return false
	}
	beforeOK := idx == 0 || !isPathTokenChar(rune(narrative[idx-1]))
	after := idx + len(path)
	afterOK := after >= len(narrative) || !isPathTokenChar(rune(narrative[after]))
	return beforeOK && afterOK
}

func isPathTokenChar(c rune) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') ||
		c == '/' || c == '_' || c == '-' || c == '.'
}

func narrativeVariants(cite evidence.ObservedCitation, norm evidence.FileRegionNormalizer) []string {
	switch cite.Kind {
	case leakKindURL:
		return []string{cite.Token}
	case leakKindPathLine:
		path := norm.Normalize(cite.Path)
		return []string{
			pathLineKey(path, cite.Line),
			pathLineKey(filepath.ToSlash(cite.Path), cite.Line),
			cite.Token,
		}
	default:
		path := norm.Normalize(cite.Path)
		if path == "" {
			path = cite.Token
		}
		return []string{path, cite.Token, filepath.ToSlash(cite.Token)}
	}
}

func observedInTypedChannel(typed TypedChannel, cite evidence.ObservedCitation, norm evidence.FileRegionNormalizer) bool {
	switch cite.Kind {
	case leakKindURL:
		return typedHasURL(typed, cite.URL)
	case leakKindPathLine:
		path := norm.Normalize(cite.Path)
		return typedHasPathLine(typed, path, cite.Line)
	default:
		path := norm.Normalize(cite.Path)
		return typedHasPath(typed, path)
	}
}

// BuildWorkerTypedChannel collects typed citation tokens from a worker completion report.
func BuildWorkerTypedChannel(
	roots evidence.CitationRoots,
	ev evidence.Ledger,
	findings []WorkerFindingInput,
	citedURLs []string,
) TypedChannel {
	ch := TypedChannel{
		Paths:     map[string]struct{}{},
		PathLines: map[string]struct{}{},
		URLs:      map[string]struct{}{},
	}
	for _, finding := range findings {
		path := strings.TrimSpace(finding.Path)
		if path == "" {
			handle := strings.TrimSpace(finding.Evidence)
			if handle == "" {
				continue
			}
			rec, ok := evidence.ResolveHandle(ev, handle)
			if !ok || strings.TrimSpace(rec.Path) == "" {
				continue
			}
			path = filepath.ToSlash(strings.TrimSpace(rec.Path))
		} else if rel := normalizedReportPath(roots, path, nil); rel != "" {
			path = rel
		} else {
			path = filepath.ToSlash(path)
		}
		ch.Paths[path] = struct{}{}
		if finding.Line > 0 {
			ch.PathLines[pathLineKey(path, finding.Line)] = struct{}{}
		}
	}
	for _, url := range citedURLs {
		url = strings.TrimSpace(url)
		if url != "" {
			ch.URLs[url] = struct{}{}
		}
	}
	return ch
}

// EvaluateWorkerProseLeaks separates typed duplicates from advisory citations.
func EvaluateWorkerProseLeaks(narrative WorkerNarrativeInput, ev evidence.Ledger, typed TypedChannel) ProseLeakEval {
	blob := joinWorkerNarrative(narrative)
	leaks := ProseLeaks(blob, ev, typed)
	var advisory, dup []string
	advSeen := map[string]struct{}{}
	dupSeen := map[string]struct{}{}
	for _, leak := range leaks {
		if leak.AlsoInTyped {
			addOffender(&dup, dupSeen, leak.Token)
			continue
		}
		addOffender(&advisory, advSeen, leak.Token)
	}
	dup = collapsePathLineTokens(dup)
	advisory = collapsePathLineTokens(advisory)
	return ProseLeakEval{
		Leaks:              leaks,
		AdvisoryLeakTokens: advisory,
		DuplicateTokens:    dup,
	}
}

func joinWorkerNarrative(narrative WorkerNarrativeInput) string {
	var parts []string
	if s := strings.TrimSpace(narrative.Brief); s != "" {
		parts = append(parts, s)
	}
	for _, item := range narrative.ObjectivesMet {
		if s := strings.TrimSpace(item); s != "" {
			parts = append(parts, s)
		}
	}
	for _, item := range narrative.RemainingRisk {
		if s := strings.TrimSpace(item); s != "" {
			parts = append(parts, s)
		}
	}
	for _, item := range narrative.FindingNotes {
		if s := strings.TrimSpace(item); s != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, "\n")
}

func typedHasPath(typed TypedChannel, path string) bool {
	path = filepath.ToSlash(strings.TrimSpace(path))
	if path == "" {
		return false
	}
	if _, ok := typed.Paths[path]; ok {
		return true
	}
	if norm, ok := evidence.NormalizeCitationPath("", path); ok {
		if _, ok := typed.Paths[norm]; ok {
			return true
		}
	}
	fileNorm := evidence.FileRegionNormalizer{}
	if normPath := fileNorm.Normalize(path); normPath != path {
		if _, ok := typed.Paths[normPath]; ok {
			return true
		}
	}
	return false
}

func typedHasPathLine(typed TypedChannel, path string, line int) bool {
	path = filepath.ToSlash(strings.TrimSpace(path))
	if path == "" || line <= 0 {
		return false
	}
	if _, ok := typed.PathLines[pathLineKey(path, line)]; ok {
		return true
	}
	if norm, ok := evidence.NormalizeCitationPath("", path); ok {
		if _, ok := typed.PathLines[pathLineKey(norm, line)]; ok {
			return true
		}
	}
	fileNorm := evidence.FileRegionNormalizer{}
	if normPath := fileNorm.Normalize(path); normPath != path {
		if _, ok := typed.PathLines[pathLineKey(normPath, line)]; ok {
			return true
		}
	}
	return typedHasPath(typed, path)
}

func typedHasURL(typed TypedChannel, url string) bool {
	url = strings.TrimSpace(url)
	if url == "" {
		return false
	}
	_, ok := typed.URLs[url]
	return ok
}

func pathLineKey(path string, line int) string {
	return fmt.Sprintf("%s:%d", filepath.ToSlash(strings.TrimSpace(path)), line)
}

// collapsePathLineTokens drops bare path tokens when a path:line token shares the same path prefix.
func collapsePathLineTokens(tokens []string) []string {
	if len(tokens) <= 1 {
		return tokens
	}
	pathLines := map[string]struct{}{}
	for _, token := range tokens {
		if path, _, ok := evidence.SplitPathLineToken(token); ok {
			pathLines[path] = struct{}{}
		}
	}
	if len(pathLines) == 0 {
		return tokens
	}
	out := make([]string, 0, len(tokens))
	for _, token := range tokens {
		if _, _, ok := evidence.SplitPathLineToken(token); ok {
			out = append(out, token)
			continue
		}
		if _, hasLine := pathLines[token]; hasLine {
			continue
		}
		out = append(out, token)
	}
	return out
}
