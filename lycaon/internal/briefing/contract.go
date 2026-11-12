package briefing

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Contract bounds one section's output. It is part of the manifest, so a
// changed contract is a changed question.
type Contract struct {
	MaxSentences int `json:"max_sentences"`
	MaxRunes     int `json:"max_runes"`
}

var (
	// OneSentence is the approval, Walk and line-facts contract.
	OneSentence = Contract{MaxSentences: 1, MaxRunes: 180}
	// TwoSentences is the opened-activity change contract.
	TwoSentences = Contract{MaxSentences: 2, MaxRunes: 280}
)

// ErrNoText reports output with nothing usable once markup is removed.
var ErrNoText = errors.New("briefing output has no usable text")

var (
	listMarker   = regexp.MustCompile(`^\s*(?:[-*+•]|\d+[.)])\s+`)
	headingMark  = regexp.MustCompile(`^\s*#{1,6}\s+`)
	emphasis     = regexp.MustCompile(`(\*\*|__)(.+?)(\*\*|__)`)
	markdownLink = regexp.MustCompile(`\[([^\]]+)\]\([^)]*\)`)
)

// Parse fits model output to the contract: plain text with single-backtick
// identifiers kept, at most MaxSentences whole sentences, at most MaxRunes.
func (c Contract) Parse(raw string) (string, error) {
	if c.MaxSentences <= 0 || c.MaxRunes <= 0 {
		return "", fmt.Errorf("briefing contract %+v is unbounded", c)
	}
	text := plainText(raw)
	if !strings.ContainsFunc(text, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }) {
		return "", ErrNoText
	}
	sentences := splitSentences(text)
	kept := ""
	for _, sentence := range sentences[:min(len(sentences), c.MaxSentences)] {
		next := strings.TrimSpace(kept + " " + sentence)
		if kept != "" && utf8.RuneCountInString(next) > c.MaxRunes {
			break
		}
		kept = next
	}
	return clampWords(kept, c.MaxRunes), nil
}

func plainText(raw string) string {
	lines := strings.Split(raw, "\n")
	for i, line := range lines {
		line = listMarker.ReplaceAllString(line, "")
		line = headingMark.ReplaceAllString(line, "")
		lines[i] = line
	}
	text := strings.Join(lines, " ")
	text = markdownLink.ReplaceAllString(text, "$1")
	text = emphasis.ReplaceAllString(text, "$2")
	text = strings.Join(strings.Fields(text), " ")
	text = strings.Trim(text, `"'“”‘’ `)
	if strings.Count(text, "`")%2 == 1 {
		text = strings.ReplaceAll(text, "`", "")
	}
	return strings.TrimSpace(text)
}

// splitSentences ends a sentence at terminal punctuation followed by space and
// an uppercase, digit or code start, or by the end of text, outside backtick
// code spans.
func splitSentences(text string) []string {
	runes := []rune(text)
	var out []string
	start, inCode := 0, false
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if r == '`' {
			inCode = !inCode
			continue
		}
		if inCode || !isTerminator(r) {
			continue
		}
		end := i
		for end+1 < len(runes) && isTerminator(runes[end+1]) {
			end++
		}
		next := end + 1
		if next < len(runes) && !unicode.IsSpace(runes[next]) {
			i = end
			continue
		}
		for next < len(runes) && unicode.IsSpace(runes[next]) {
			next++
		}
		if next < len(runes) && !unicode.IsUpper(runes[next]) && !unicode.IsDigit(runes[next]) && runes[next] != '`' {
			i = end
			continue
		}
		out = append(out, strings.TrimSpace(string(runes[start:end+1])))
		start, i = next, next-1
	}
	if rest := strings.TrimSpace(string(runes[min(start, len(runes)):])); rest != "" {
		out = append(out, rest)
	}
	return out
}

func isTerminator(r rune) bool { return r == '.' || r == '!' || r == '?' }

// clampWords cuts at a word boundary within max runes and marks the cut.
func clampWords(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	runes := []rune(s)[:max-1]
	for i := len(runes) - 1; i > len(runes)/2; i-- {
		if unicode.IsSpace(runes[i]) {
			runes = runes[:i]
			break
		}
	}
	return strings.TrimRightFunc(string(runes), unicode.IsSpace) + "…"
}
