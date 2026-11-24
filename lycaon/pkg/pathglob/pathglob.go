// Package pathglob defines the repo-relative glob vocabulary shared by task
// scopes, sandbox profiles, workspace snapshots, and overlay promotion.
package pathglob

import (
	"path/filepath"
	"strings"
)

type compiledRule struct {
	matchAll bool
	segments []string
}

func (r compiledRule) match(cand Candidate) bool {
	if r.matchAll {
		return true
	}
	return matchSegments(r.segments, cand.segments)
}

// RuleSet holds an immutable list of pre-split, POSIX-translated glob rules.
type RuleSet struct {
	rules []compiledRule
}

// Candidate holds the slash-normalized segments of one candidate path.
type Candidate struct {
	segments []string
}

// Len reports how many non-empty patterns the rule set compiled.
func (s RuleSet) Len() int { return len(s.rules) }

// Segments returns a copy of the candidate's split path segments.
func (c Candidate) Segments() []string {
	if len(c.segments) == 0 {
		return nil
	}
	out := make([]string, len(c.segments))
	copy(out, c.segments)
	return out
}

// Compile prepares an immutable rule set sharing the scalar segment matcher.
func Compile(patterns []string) RuleSet {
	if len(patterns) == 0 {
		return RuleSet{}
	}
	rules := make([]compiledRule, 0, len(patterns))
	for _, raw := range patterns {
		if rule, ok := compilePattern(raw); ok {
			rules = append(rules, rule)
		}
	}
	return RuleSet{rules: rules}
}

func compilePattern(raw string) (compiledRule, bool) {
	pattern := filepath.ToSlash(strings.TrimSpace(raw))
	if pattern == "" {
		return compiledRule{}, false
	}
	if pattern == "**" {
		return compiledRule{matchAll: true}, true
	}
	segments := strings.Split(pattern, "/")
	for i, seg := range segments {
		if seg != "**" {
			segments[i] = translatePOSIXClass(seg)
		}
	}
	return compiledRule{segments: segments}, true
}

// PrepareCandidate normalizes and splits relPath once for evaluation across rule sets.
func PrepareCandidate(relPath string) Candidate {
	relPath = filepath.ToSlash(strings.TrimSpace(relPath))
	if relPath == "" {
		return Candidate{}
	}
	return Candidate{segments: strings.Split(relPath, "/")}
}

// MatchCandidate reports whether cand matches any rule in s.
func (s RuleSet) MatchCandidate(cand Candidate) bool {
	for i := range s.rules {
		if s.rules[i].match(cand) {
			return true
		}
	}
	return false
}

// Match reports whether relPath matches any rule in s.
func (s RuleSet) Match(relPath string) bool {
	return s.MatchCandidate(PrepareCandidate(relPath))
}

// Match reports whether relPath matches pattern. Patterns use slash-separated
// POSIX segments; ** matches zero or more segments, except a trailing ** must
// consume at least one segment.
func Match(pattern, relPath string) bool {
	rule, ok := compilePattern(pattern)
	if !ok {
		return false
	}
	return rule.match(PrepareCandidate(relPath))
}

// Covers reports task-scope coverage. A non-glob path names both itself and
// its descendants; a glob uses Match exactly.
func Covers(pattern, relPath string) bool {
	pattern = normalizePattern(pattern)
	relPath = NormalizeRel(relPath)
	if pattern == "" || relPath == "" {
		return false
	}
	if pattern == "." {
		return true
	}
	if strings.ContainsAny(pattern, "*?[") {
		return Match(pattern, relPath)
	}
	return relPath == pattern || strings.HasPrefix(relPath, pattern+"/")
}

// NormalizeRel returns a clean slash-form relative path. Paths that are empty,
// name the root, or escape it return empty.
func NormalizeRel(value string) string {
	value = filepath.ToSlash(strings.TrimSpace(value))
	value = strings.TrimPrefix(value, "./")
	value = strings.Trim(value, "/")
	if value == "" || value == "." || value == ".." || strings.HasPrefix(value, "../") {
		return ""
	}
	return value
}

func normalizePattern(value string) string {
	value = filepath.ToSlash(strings.TrimSpace(value))
	if value == "." || value == "./" {
		return "."
	}
	value = strings.TrimPrefix(value, "./")
	value = strings.Trim(value, "/")
	if value == "" || value == ".." || strings.HasPrefix(value, "../") {
		return ""
	}
	return value
}

func matchSegments(pattern, path []string) bool {
	if len(pattern) == 0 {
		return len(path) == 0
	}
	segment := pattern[0]
	if segment == "**" {
		if len(pattern) == 1 {
			return len(path) >= 1
		}
		for consumed := 0; consumed <= len(path); consumed++ {
			if matchSegments(pattern[1:], path[consumed:]) {
				return true
			}
		}
		return false
	}
	if len(path) == 0 {
		return false
	}
	ok, err := filepath.Match(segment, path[0])
	return err == nil && ok && matchSegments(pattern[1:], path[1:])
}

func translatePOSIXClass(pattern string) string {
	if !strings.ContainsRune(pattern, '[') {
		return pattern
	}
	var out strings.Builder
	for i := 0; i < len(pattern); i++ {
		if pattern[i] == '\\' && i+1 < len(pattern) {
			out.WriteByte(pattern[i])
			i++
			out.WriteByte(pattern[i])
			continue
		}
		out.WriteByte(pattern[i])
		if pattern[i] == '[' && i+1 < len(pattern) && pattern[i+1] == '!' {
			i++
			out.WriteByte('^')
		}
	}
	return out.String()
}
