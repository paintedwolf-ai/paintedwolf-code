// Package grep implements a structural code search engine.
//
// The query language supports finding code patterns, optionally scoped to a
// language, with constraint and replacement blocks:
//
//	find <lang>::<code-pattern> [where { <constraints> }] [replace { <template> }]
//
// The find keyword and language prefix are both optional.
package grep

import (
	"errors"
	"fmt"
	"strings"
)

// Query is the parsed representation of a structural grep query.
type Query struct {
	// Lang is the language name (e.g. "go", "rust", "sexp"), or empty if
	// unspecified.
	Lang string

	// Pattern is the code pattern or S-expression to match against.
	Pattern string

	// Where is the raw constraint block content (text between the braces in
	// a where { ... } clause), or empty if no where clause was given.
	Where string

	// Replace is the raw replacement template content (text between the
	// braces in a replace { ... } clause), or empty if no replace clause
	// was given.
	Replace string
}

// String returns the canonical string form of the query.
func (q Query) String() string {
	var b strings.Builder
	b.WriteString("find ")
	if q.Lang != "" {
		b.WriteString(q.Lang)
		b.WriteString("::")
	}
	b.WriteString(q.Pattern)
	if q.Where != "" {
		b.WriteString(" where { ")
		b.WriteString(q.Where)
		b.WriteString(" }")
	}
	if q.Replace != "" {
		b.WriteString(" replace { ")
		b.WriteString(q.Replace)
		b.WriteString(" }")
	}
	return b.String()
}

// ParseQuery parses a structural grep query string into a [Query].
//
// Accepted forms:
//
//	find go::func $NAME($$$) error           — full form
//	go::func $NAME($$$) error                — shorthand (no find keyword)
//	func $NAME($$$) error                    — bare pattern (no language prefix)
//	find sexp::(function_definition)         — S-expression mode
//
// The where { ... } and replace { ... } blocks are optional and support
// nested braces.
func ParseQuery(input string) (Query, error) {
	s := strings.TrimSpace(input)
	if s == "" {
		return Query{}, errors.New("empty query")
	}

	// Strip optional "find" keyword.
	if hasFindPrefix(s) {
		after := strings.TrimSpace(s[4:])
		if after == "" {
			return Query{}, errors.New("incomplete query: find keyword with no pattern")
		}
		s = after
	}

	// Extract where and replace blocks from the tail, working from the
	// remaining string. We need to find the boundary between the pattern
	// and the keyword blocks. We scan for top-level " where " or
	// " replace " tokens.
	pattern, where, replace, err := splitBlocks(s)
	if err != nil {
		return Query{}, err
	}

	// Parse lang::pattern.
	lang, pat := splitLang(pattern)

	return Query{
		Lang:    lang,
		Pattern: pat,
		Where:   where,
		Replace: replace,
	}, nil
}

// hasFindPrefix reports whether s starts with the word "find" followed by
// whitespace (or end of string).
func hasFindPrefix(s string) bool {
	if !strings.HasPrefix(s, "find") {
		return false
	}
	if len(s) == 4 {
		return true
	}
	// Must be followed by whitespace to be the keyword, not part of a
	// pattern like "findAll".
	return s[4] == ' ' || s[4] == '\t' || s[4] == '\n'
}

// splitLang splits a "lang::rest" prefix, returning ("lang", "rest").
// If no :: is found, returns ("", s).
// Only the first :: is considered, and the lang must be a simple identifier
// (letters, digits, underscores, hyphens).
func splitLang(s string) (string, string) {
	idx := strings.Index(s, "::")
	if idx < 0 {
		return "", s
	}
	candidate := s[:idx]
	if candidate == "" || !isLangName(candidate) {
		return "", s
	}
	return candidate, s[idx+2:]
}

// isLangName reports whether s is a valid language identifier.
func isLangName(s string) bool {
	for _, c := range s {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

// splitBlocks splits the input into (pattern, where-content, replace-content).
// It looks for top-level " where {" and " replace {" boundaries, respecting
// brace nesting.
func splitBlocks(s string) (pattern, where, replace string, err error) {
	// Find the position of top-level " where " keyword.
	whereIdx := findKeyword(s, "where")
	replaceIdx := findKeyword(s, "replace")

	// Reject replace-before-where ordering; the spec requires
	// find ... where ... replace ...
	if whereIdx >= 0 && replaceIdx >= 0 && replaceIdx < whereIdx {
		return "", "", "", errors.New("replace block must come after where block")
	}

	// Determine the end of the pattern portion.
	patEnd := len(s)
	if whereIdx >= 0 {
		patEnd = whereIdx
	}
	if replaceIdx >= 0 && replaceIdx < patEnd {
		patEnd = replaceIdx
	}

	pattern = strings.TrimSpace(s[:patEnd])

	// Parse where block if present.
	if whereIdx >= 0 {
		blockStart := whereIdx + len("where")
		rest := strings.TrimSpace(s[blockStart:])
		content, consumed, err2 := extractBraceBlock(rest)
		if err2 != nil {
			return "", "", "", fmt.Errorf("where block: %w", err2)
		}
		where = strings.TrimSpace(content)

		// After the where block, check for a replace block.
		afterWhere := strings.TrimSpace(rest[consumed:])
		if afterWhere != "" {
			rpIdx := findKeywordAtStart(afterWhere, "replace")
			if rpIdx < 0 {
				// Trailing garbage — include it in pattern? No, treat as error.
				// Actually, be lenient: this shouldn't happen if the query is well-formed.
				return "", "", "", fmt.Errorf("unexpected content after where block: %q", afterWhere)
			}
			rpRest := strings.TrimSpace(afterWhere[len("replace"):])
			content2, _, err3 := extractBraceBlock(rpRest)
			if err3 != nil {
				return "", "", "", fmt.Errorf("replace block: %w", err3)
			}
			replace = strings.TrimSpace(content2)
		}
	} else if replaceIdx >= 0 {
		blockStart := replaceIdx + len("replace")
		rest := strings.TrimSpace(s[blockStart:])
		content, consumed, err2 := extractBraceBlock(rest)
		if err2 != nil {
			return "", "", "", fmt.Errorf("replace block: %w", err2)
		}
		replace = strings.TrimSpace(content)

		// Check for trailing content after the replace block.
		afterReplace := strings.TrimSpace(rest[consumed:])
		if afterReplace != "" {
			return "", "", "", fmt.Errorf("unexpected content after replace block: %q", afterReplace)
		}
	}

	return pattern, where, replace, nil
}

// findKeyword returns the index of a top-level keyword in s.
// A keyword must be preceded by whitespace and followed by whitespace or '{'.
// Returns -1 if not found.
func findKeyword(s, keyword string) int {
	search := s
	offset := 0
	for {
		idx := strings.Index(search, keyword)
		if idx < 0 {
			return -1
		}
		absIdx := offset + idx

		// Must be preceded by whitespace (or start of string, but keywords
		// come after a pattern so require whitespace).
		if absIdx == 0 || !isSpace(s[absIdx-1]) {
			search = search[idx+len(keyword):]
			offset = absIdx + len(keyword)
			continue
		}

		// Must be followed by whitespace or '{' or end.
		afterIdx := absIdx + len(keyword)
		if afterIdx < len(s) && !isSpace(s[afterIdx]) && s[afterIdx] != '{' {
			search = search[idx+len(keyword):]
			offset = absIdx + len(keyword)
			continue
		}

		return absIdx
	}
}

// findKeywordAtStart checks if the string starts with the given keyword,
// optionally preceded by whitespace.
func findKeywordAtStart(s, keyword string) int {
	trimmed := strings.TrimSpace(s)
	if strings.HasPrefix(trimmed, keyword) {
		after := len(keyword)
		if after >= len(trimmed) || isSpace(trimmed[after]) || trimmed[after] == '{' {
			return 0
		}
	}
	return -1
}

func isSpace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r'
}

// extractBraceBlock extracts content from a brace-delimited block.
// It expects the input to start with optional whitespace then '{'.
// Returns the content between the braces (excluding the braces themselves),
// and the number of bytes consumed from the input (including the closing brace).
func extractBraceBlock(s string) (content string, consumed int, err error) {
	trimmed := strings.TrimLeftFunc(s, func(r rune) bool {
		return r == ' ' || r == '\t' || r == '\n' || r == '\r'
	})
	leading := len(s) - len(trimmed)

	if len(trimmed) == 0 || trimmed[0] != '{' {
		return "", 0, errors.New("expected '{'")
	}

	depth := 0
	start := leading // position of '{' in original s
	for i := start; i < len(s); i++ {
		switch s[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				// Content is between the opening '{' and this '}'.
				inner := s[start+1 : i]
				return inner, i + 1, nil
			}
		}
	}

	return "", 0, errors.New("unmatched '{'")
}
