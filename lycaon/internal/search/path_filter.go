package search

import "strings"

// pathFilterMatch applies ASCII-folded path, directory, basename, or wildcard matching.
func pathFilterMatch(pattern, candidate string) bool {
	pattern = asciiLower(pathFilterPattern(pattern))
	candidate = asciiLower(strings.TrimPrefix(candidate, "/"))
	if strings.ContainsRune(pattern, '*') {
		return wildcardPathMatch(pattern, candidate)
	}
	return candidate == pattern ||
		strings.HasPrefix(candidate, pattern+"/") ||
		strings.HasSuffix(candidate, "/"+pattern)
}

// pathFilterSQL is the store-leg half of the path: semantics.
func pathFilterSQL(value string) (string, []any) {
	value = pathFilterPattern(value)
	if strings.ContainsRune(value, '*') {
		return `e.path LIKE ? ESCAPE '\'`, []any{globToLike(value)}
	}
	escaped := escapeLike(value)
	return `(e.path = ? COLLATE NOCASE OR e.path LIKE ? ESCAPE '\' OR e.path LIKE ? ESCAPE '\')`,
		[]any{value, escaped + "/%", "%/" + escaped}
}

func pathFilterPattern(value string) string {
	value = strings.TrimPrefix(strings.TrimSpace(value), "/")
	if strings.HasSuffix(value, "/") {
		return value + "*"
	}
	return value
}

// wildcardPathMatch runs a '*' glob over the whole path via a boolean DP row.
func wildcardPathMatch(pattern, candidate string) bool {
	candidateRunes := []rune(candidate)
	rows := make([]bool, len(candidateRunes)+1)
	rows[0] = true
	for _, token := range pattern {
		next := make([]bool, len(candidateRunes)+1)
		if token == '*' {
			next[0] = rows[0]
			for i := 1; i <= len(candidateRunes); i++ {
				next[i] = rows[i] || next[i-1]
			}
		} else {
			for i := 1; i <= len(candidateRunes); i++ {
				next[i] = rows[i-1] && candidateRunes[i-1] == token
			}
		}
		rows = next
	}
	return rows[len(candidateRunes)]
}

// asciiLower folds A-Z only, matching SQLite's LIKE and NOCASE collation.
func asciiLower(s string) string {
	hasUpper := false
	for i := 0; i < len(s); i++ {
		if s[i] >= 'A' && s[i] <= 'Z' {
			hasUpper = true
			break
		}
	}
	if !hasUpper {
		return s
	}
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + ('a' - 'A')
		}
	}
	return string(b)
}

// escapeLike protects LIKE metacharacters, the escape byte first.
func escapeLike(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, "%", `\%`)
	s = strings.ReplaceAll(s, "_", `\_`)
	return s
}

// globToLike renders a '*' wildcard value as a LIKE pattern.
func globToLike(glob string) string {
	glob = escapeLike(glob)
	return strings.ReplaceAll(glob, "*", "%")
}
