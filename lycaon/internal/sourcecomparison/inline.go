package sourcecomparison

import (
	"unicode"

	"github.com/aymanbagabas/go-udiff/lcs"
	"github.com/lycaon/lycaon/pkg/api"
)

const inlineRefineLimit = 64 * 1024

func inlineChanges(a, b string) ([]api.SourceReaderSpan, []api.SourceReaderSpan) {
	// Large replacements retain line shading without unbounded inline refinement.
	if len(a)+len(b) > inlineRefineLimit || a == "" || b == "" {
		return nil, nil
	}
	ids := make(map[string]rune)
	var lastID rune
	encode := func(text string) ([]rune, []int) {
		var tokens []rune
		offsets := []int{0}
		start, word := 0, false
		appendToken := func(end int) {
			token := text[start:end]
			id, found := ids[token]
			if !found {
				lastID++
				id = lastID
				ids[token] = id
			}
			tokens = append(tokens, id)
			offsets = append(offsets, offsets[len(offsets)-1]+width(token))
			start = end
		}
		for at, r := range text {
			isWord := wordRune(r)
			if at > start && (!isWord || !word) {
				appendToken(at)
			}
			word = isWord
		}
		if start < len(text) {
			appendToken(len(text))
		}
		return tokens, offsets
	}
	at, ao := encode(a)
	bt, bo := encode(b)
	changes := lcs.DiffRunes(at, bt)
	var old, next []api.SourceReaderSpan
	for _, change := range changes {
		left := api.SourceReaderSpan{From: ao[change.Start], To: ao[change.End]}
		right := api.SourceReaderSpan{From: bo[change.ReplStart], To: bo[change.ReplEnd]}
		// Tiny unchanged islands merge adjacent highlights.
		if n := len(old); n > 0 && left.From-old[n-1].To <= 3 && right.From-next[n-1].To <= 3 {
			old[n-1].To, next[n-1].To = left.To, right.To
		} else {
			old = append(old, left)
			next = append(next, right)
		}
	}
	return old, next
}

func inlineRowSpans(spans []api.SourceReaderSpan, offset, size int) []api.SourceReaderSpan {
	out := []api.SourceReaderSpan{}
	for _, span := range spans {
		if span.To > offset && span.From < offset+size {
			out = append(out, api.SourceReaderSpan{From: max(0, span.From-offset), To: min(size, span.To-offset)})
		}
	}
	return out
}

func keepsUnchangedWord(text string, spans []api.SourceReaderSpan) bool {
	at, next := 0, 0
	for _, r := range text {
		for next < len(spans) && spans[next].To <= at {
			next++
		}
		covered := next < len(spans) && spans[next].From <= at
		if !covered && wordRune(r) {
			return true
		}
		at++
		if r > 0xffff {
			at++
		}
	}
	return false
}

func wordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsNumber(r) || unicode.IsMark(r) || r == '_'
}
