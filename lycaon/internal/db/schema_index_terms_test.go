package db

import (
	"fmt"
	"strings"
)

// SQLite marks every expression key with cid=-2; its declared SQL supplies identity.
func sqliteIndexTerms(definition string) ([]string, error) {
	tokens, err := sqliteIndexTokens(definition)
	if err != nil {
		return nil, err
	}
	var terms []string
	var term []string
	depth := 0
	appendTerm := func() {
		if n := len(term); n > 0 && (term[n-1] == "asc" || term[n-1] == "desc") {
			term = term[:n-1]
		}
		if n := len(term); n >= 2 && term[n-2] == "collate" {
			term = term[:n-2]
		}
		terms = append(terms, strings.Join(term, " "))
		term = nil
	}
	for _, token := range tokens {
		switch token {
		case "(":
			depth++
			if depth == 1 {
				continue
			}
		case ")":
			depth--
			if depth == 0 {
				appendTerm()
				return terms, nil
			}
		case ",":
			if depth == 1 {
				appendTerm()
				continue
			}
		}
		if depth > 0 {
			term = append(term, token)
		}
	}
	return nil, fmt.Errorf("index definition has no complete key list: %s", definition)
}

func sqliteIndexTokens(source string) ([]string, error) {
	var tokens []string
	for i := 0; i < len(source); {
		c := source[i]
		if strings.ContainsRune(" \t\r\n\f", rune(c)) {
			i++
			continue
		}
		if strings.HasPrefix(source[i:], "--") {
			for i < len(source) && source[i] != '\n' {
				i++
			}
			continue
		}
		if strings.HasPrefix(source[i:], "/*") {
			end := strings.Index(source[i+2:], "*/")
			if end < 0 {
				return nil, fmt.Errorf("unterminated index comment")
			}
			i += end + 4
			continue
		}
		start := i
		if c == '\'' || c == '"' || c == '`' || c == '[' {
			close := c
			if c == '[' {
				close = ']'
			}
			i++
			closed := false
			for i < len(source) {
				if source[i] != close {
					i++
					continue
				}
				i++
				if c != '[' && i < len(source) && source[i] == close {
					i++
					continue
				}
				closed = true
				break
			}
			if !closed {
				return nil, fmt.Errorf("unterminated quoted index token")
			}
			tokens = append(tokens, source[start:i])
			continue
		}
		if indexWordByte(c) {
			for i < len(source) && indexWordByte(source[i]) {
				i++
			}
			token := []byte(source[start:i])
			for j, value := range token {
				if value >= 'A' && value <= 'Z' {
					token[j] += 'a' - 'A'
				}
			}
			tokens = append(tokens, string(token))
			continue
		}
		tokens = append(tokens, source[i:i+1])
		i++
	}
	return tokens, nil
}

func indexWordByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '$' || c >= 0x80
}
