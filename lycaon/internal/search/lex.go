package search

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

type tokenKind int

const (
	tokEOF tokenKind = iota
	tokAnd
	tokOr
	tokNot
	tokLParen
	tokRParen
	tokFilter
	tokText
)

type token struct {
	kind   tokenKind
	text   string
	field  string
	pos    int
	quoted bool
}

// lex tokenizes a query. Unknown punctuation separates words, an open quote
// runs to the end, a stray ')' is dropped; only a recognized filter can error.
// When a query contains no word or quoted text terms, bare non-grammar
// punctuation runs (such as "==" or "!=") are preserved as literal text terms.
func lex(query string) ([]token, error) {
	var out []token
	var skippedPunct []token
	hasText := false
	i := 0
	depth := 0
	for {
		i = skipSpace(query, i)
		if i >= len(query) {
			if !hasText && len(skippedPunct) > 0 {
				out = mergeTokensByPos(out, skippedPunct)
			}
			out = append(out, token{kind: tokEOF, pos: i})
			return out, nil
		}
		pos := i
		switch query[i] {
		case '(':
			depth++
			out = append(out, token{kind: tokLParen, pos: pos})
			i++
			continue
		case ')':
			if depth > 0 {
				depth--
				out = append(out, token{kind: tokRParen, pos: pos})
			}
			i++
			continue
		case '"', '\'':
			text, next := readQuoted(query, i)
			if strings.TrimSpace(text) != "" {
				hasText = true
				out = append(out, token{kind: tokText, text: text, pos: pos, quoted: true})
			}
			i = next
			continue
		}
		word, next := readWord(query, i)
		if word == "" {
			punct, pNext := readPunctRun(query, i)
			if punct != "" {
				if isBareOperatorToken(punct) {
					skippedPunct = append(skippedPunct, token{kind: tokText, text: punct, pos: pos})
				}
				i = pNext
			} else {
				_, w := utf8.DecodeRuneInString(query[i:])
				i += w
			}
			continue
		}
		// A dot ending a bare word is sentence punctuation, not part of it.
		rawWord := word
		if word = strings.TrimRight(word, "."); word == "" {
			if isBareOperatorToken(rawWord) {
				skippedPunct = append(skippedPunct, token{kind: tokText, text: rawWord, pos: pos})
			}
			i = next
			continue
		}
		// Operators are uppercase only; "not", "and", "or" are ordinary words.
		switch word {
		case "AND":
			out = append(out, token{kind: tokAnd, text: word, pos: pos})
			i = next
			continue
		case "OR":
			out = append(out, token{kind: tokOr, text: word, pos: pos})
			i = next
			continue
		case "NOT":
			out = append(out, token{kind: tokNot, text: word, pos: pos})
			i = next
			continue
		}
		if isFieldName(word) && next < len(query) && query[next] == ':' {
			field := strings.ToLower(word)
			if !isAllowlistedField(field) {
				// Field-like prose remains searchable text.
				text, end := readProseFieldRun(query, pos, next)
				hasText = true
				out = append(out, token{kind: tokText, text: text, pos: pos})
				i = end
				continue
			}
			i = next + 1
			i = skipSpace(query, i)
			if i >= len(query) {
				return nil, newParseError(pos, ParseErrEmptyValue, field, "empty value for field "+field)
			}
			valPos := i
			var value string
			switch query[i] {
			case '"', '\'':
				value, i = readQuoted(query, i)
			default:
				value, i = readFilterValue(query, i)
			}
			if strings.TrimSpace(value) == "" {
				return nil, newParseError(valPos, ParseErrEmptyValue, field, "empty value for field "+field)
			}
			out = append(out, token{kind: tokFilter, field: field, text: value, pos: pos})
			continue
		}
		hasText = true
		out = append(out, token{kind: tokText, text: word, pos: pos})
		i = next
	}
}

func isWordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-' ||
		r == '.' || r == '/' || r == '*' || r == '#' || r == '\''
}

func readPunctRun(s string, i int) (punct string, next int) {
	start := i
	for i < len(s) {
		r, w := utf8.DecodeRuneInString(s[i:])
		if unicode.IsSpace(r) || r == '(' || r == ')' || r == '"' || r == '\'' || isWordRune(r) {
			break
		}
		i += w
	}
	return s[start:i], i
}

// isBareOperatorToken reports whether s is a recognized source-code punctuation
// operator that should remain searchable when pasted without word terms, while
// keeping arbitrary stray delimiters (such as "[", "{}", "!?+") rejected.
func isBareOperatorToken(s string) bool {
	switch s {
	case "==", "===", "!=", "!==", "<=", ">=", "::", ":=", "=>", "&&", "||", "??", "<<", ">>", "+=", "-=", "*=", "/=", "%=", "...":
		return true
	default:
		return false
	}
}

func mergeTokensByPos(a, b []token) []token {
	merged := make([]token, 0, len(a)+len(b))
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		if a[i].pos <= b[j].pos {
			merged = append(merged, a[i])
			i++
		} else {
			merged = append(merged, b[j])
			j++
		}
	}
	merged = append(merged, a[i:]...)
	merged = append(merged, b[j:]...)
	return merged
}

func isFieldName(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		if i == 0 && !unicode.IsLetter(r) {
			return false
		}
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' {
			return false
		}
	}
	return true
}

func isAllowlistedField(field string) bool {
	for _, f := range dslFieldAllowlist {
		if f == field {
			return true
		}
	}
	return false
}

func readWord(s string, i int) (word string, next int) {
	start := i
	for i < len(s) {
		r, w := utf8.DecodeRuneInString(s[i:])
		// Apostrophe keeps contractions ("don't") as one free-text token.
		if isWordRune(r) {
			i += w
			continue
		}
		break
	}
	return s[start:i], i
}

// readProseFieldRun consumes word:… as free text when word is not a DSL field.
func readProseFieldRun(s string, wordStart, colonAt int) (text string, end int) {
	i := colonAt + 1
	if i >= len(s) {
		return s[wordStart:colonAt] + ":", i
	}
	r, _ := utf8.DecodeRuneInString(s[i:])
	if unicode.IsSpace(r) {
		// "Error: failed" → text "Error:" then later token "failed".
		return s[wordStart : colonAt+1], i
	}
	value, valueEnd := readFilterValue(s, i)
	return s[wordStart:colonAt] + ":" + value, valueEnd
}

func readFilterValue(s string, i int) (value string, next int) {
	start := i
	for i < len(s) {
		r, w := utf8.DecodeRuneInString(s[i:])
		if unicode.IsSpace(r) || r == ')' {
			break
		}
		if strings.HasPrefix(s[i:], "AND") && isTokenBoundary(s, i, 3) {
			break
		}
		if strings.HasPrefix(s[i:], "OR") && isTokenBoundary(s, i, 2) {
			break
		}
		if strings.HasPrefix(s[i:], "NOT") && isTokenBoundary(s, i, 3) {
			break
		}
		i += w
	}
	return s[start:i], i
}

func isTokenBoundary(s string, start, width int) bool {
	before := start == 0 || unicode.IsSpace(rune(s[start-1]))
	after := start+width >= len(s) || unicode.IsSpace(rune(s[start+width]))
	return before && after
}

// readQuoted reads a quoted phrase; a missing closing quote ends it at the
// end of the query.
func readQuoted(s string, i int) (text string, next int) {
	quote := s[i]
	i++
	var b strings.Builder
	for i < len(s) {
		if s[i] == quote {
			return b.String(), i + 1
		}
		// Backslash escapes the quote character and itself; any other pair
		// stays literal so Windows paths survive unescaped.
		if s[i] == '\\' && i+1 < len(s) && (s[i+1] == quote || s[i+1] == '\\') {
			b.WriteByte(s[i+1])
			i += 2
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String(), i
}

func skipSpace(s string, i int) int {
	for i < len(s) {
		r, w := utf8.DecodeRuneInString(s[i:])
		if !unicode.IsSpace(r) {
			break
		}
		i += w
	}
	return i
}

func newParseError(offset int, kind ParseErrorKind, field, message string) *ParseError {
	return &ParseError{Offset: offset, Kind: kind, Field: field, Message: message}
}
