// Package docrefs extracts candidate file paths from Markdown links and code spans.
package docrefs

import (
	"regexp"
	"strings"

	"github.com/lycaon/lycaon/internal/sourceloc"
)

// maxCandidates bounds downstream filesystem checks.
const maxCandidates = 20

var (
	// mdLink captures a Markdown link target.
	mdLink = regexp.MustCompile(`\]\(([^)\s]+)\)`)
	// codeSpan captures the contents of a single-line inline code span.
	codeSpan = regexp.MustCompile("`([^`\n]+)`")
)

// Extract returns bounded path candidates; callers validate file existence.
func Extract(content string) []string {
	seen := make(map[string]bool)
	out := make([]string, 0, 8)
	add := func(raw string) bool {
		tok, ok := cleanCandidate(raw)
		if !ok || seen[tok] {
			return true
		}
		seen[tok] = true
		out = append(out, tok)
		return len(out) < maxCandidates
	}
	for _, m := range mdLink.FindAllStringSubmatch(content, -1) {
		if !add(m[1]) {
			return out
		}
	}
	for _, m := range codeSpan.FindAllStringSubmatch(content, -1) {
		if !add(m[1]) {
			return out
		}
	}
	return out
}

func cleanCandidate(raw string) (string, bool) {
	tok := strings.TrimSpace(raw)
	tok = strings.Trim(tok, "<>\"'`")
	tok, _, _ = sourceloc.Cut(tok)
	if i := strings.IndexAny(tok, "#?"); i >= 0 {
		tok = tok[:i]
	}
	tok = strings.TrimRight(tok, ".,;:")
	tok = strings.TrimSuffix(tok, "/")
	tok = strings.TrimSuffix(tok, `\`)
	tok = slashRepoPath(tok)
	if tok == "" || strings.ContainsAny(tok, " \t") {
		return "", false
	}
	if strings.Contains(tok, "://") || strings.HasPrefix(tok, "mailto:") {
		return "", false
	}
	// Restrict candidates to tokens with path separators or extensions.
	if !strings.Contains(tok, "/") && !strings.Contains(tok, ".") {
		return "", false
	}
	return tok, true
}

// filepath.ToSlash preserves backslashes on Unix.
func slashRepoPath(path string) string {
	return strings.ReplaceAll(path, `\`, `/`)
}
