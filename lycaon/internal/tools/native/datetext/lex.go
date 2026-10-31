// Package datetext reads and writes date text the way the date utilities do:
// the date -d grammar, +FORMAT conversions, strptime formats and date -v
// adjustments. Every operation takes its reference time and zone explicitly.
package datetext

import (
	"strings"
)

type tokenKind int

const (
	tokNumber tokenKind = iota
	tokWord
	tokPunct
)

// token is one lexeme. A number keeps its digit count, because the grammar
// reads 4-digit and 8-digit numbers differently; signed marks a number
// written with + or -, and frac holds digits after a decimal point.
type token struct {
	kind   tokenKind
	text   string
	value  int64
	digits int
	signed bool
	frac   string
}

func (t token) isPunct(p string) bool { return t.kind == tokPunct && t.text == p }

// lex splits date text into numbers, lowercase words and punctuation. A sign
// followed by optional spaces and digits makes a signed number; a sign with
// no digits after it is dropped. Periods inside words are dropped, so a.m.
// reads as am. Spaces separate tokens and are not kept.
func lex(text string) ([]token, bool) {
	var out []token
	s := strings.ToLower(text)
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\v' || c == '\f':
			i++
		case c == '+' || c == '-':
			j := i + 1
			for j < len(s) && s[j] == ' ' {
				j++
			}
			if j >= len(s) || !isDigit(s[j]) {
				i++
				continue
			}
			number, next := lexNumber(s, j)
			number.signed = true
			if c == '-' {
				number.value = -number.value
				number.text = "-" + number.text
			}
			out = append(out, number)
			i = next
		case isDigit(c):
			number, next := lexNumber(s, i)
			out = append(out, number)
			i = next
		case isLetter(c):
			j := i
			var word strings.Builder
			for j < len(s) && (isLetter(s[j]) || s[j] == '.') {
				if s[j] != '.' {
					word.WriteByte(s[j])
				}
				j++
			}
			out = append(out, token{kind: tokWord, text: word.String()})
			i = j
		case c == ':' || c == ',' || c == '/' || c == '@' || c == '.':
			out = append(out, token{kind: tokPunct, text: string(c)})
			i++
		default:
			return nil, false
		}
	}
	return out, true
}

// lexNumber reads digits and an optional decimal fraction after . or ,.
func lexNumber(s string, i int) (token, int) {
	j := i
	for j < len(s) && isDigit(s[j]) {
		j++
	}
	t := token{kind: tokNumber, text: s[i:j], digits: j - i}
	for _, c := range s[i:j] {
		if t.value < 1e15 {
			t.value = t.value*10 + int64(c-'0')
		}
	}
	if j+1 < len(s) && (s[j] == '.' || s[j] == ',') && isDigit(s[j+1]) {
		k := j + 1
		for k < len(s) && isDigit(s[k]) {
			k++
		}
		t.frac = s[j+1 : k]
		j = k
	}
	return t, j
}

func isDigit(c byte) bool  { return c >= '0' && c <= '9' }
func isLetter(c byte) bool { return c >= 'a' && c <= 'z' }
