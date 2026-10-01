package messageview

import (
	"encoding/json"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/hostmarker"
	"github.com/lycaon/lycaon/pkg/api"
)

// readableContent decodes JSON string escapes for display. Origin positions keep
// host redaction provenance attached through indentation and Unicode decoding.
func readableContent(text string, spans []api.RedactedSpan) (string, []api.RedactedSpan) {
	_, body, _, ok := hostmarker.SplitToolJSONBody(text)
	if !ok {
		body = strings.TrimSpace(text)
		if !json.Valid([]byte(body)) || !strings.HasPrefix(body, "[") {
			return text, spans
		}
	}
	at := strings.Index(text, body)
	f := contentFormatter{}
	f.literal(text[:at], 0)
	f.json([]rune(body), utf8.RuneCountInString(text[:at]))
	f.literal(text[at+len(body):], utf8.RuneCountInString(text[:at+len(body)]))
	return string(f.text), f.spans(spans)
}

type contentFormatter struct {
	text    []rune
	origins []int
}

func (f *contentFormatter) emit(text string, origin int) {
	for _, r := range text {
		f.text = append(f.text, r)
		f.origins = append(f.origins, origin)
	}
}

func (f *contentFormatter) literal(text string, origin int) {
	for _, r := range text {
		f.text = append(f.text, r)
		f.origins = append(f.origins, origin)
		origin++
	}
}

func (f *contentFormatter) json(text []rune, base int) {
	depth := 0
	newline := func() { f.emit("\n"+strings.Repeat("  ", min(depth, 16)), -1) }
	for at := 0; at < len(text); at++ {
		r := text[at]
		switch r {
		case ' ', '\t', '\n', '\r':
			continue
		case '"':
			f.emit("\"", base+at)
			at = f.quoted(text, at+1, base)
			f.emit("\"", base+at)
		case '{', '[':
			f.emit(string(r), base+at)
			depth++
			newline()
		case '}', ']':
			depth--
			newline()
			f.emit(string(r), base+at)
		case ',':
			f.emit(",", base+at)
			newline()
		case ':':
			f.emit(": ", base+at)
		default:
			f.emit(string(r), base+at)
		}
	}
}

func (f *contentFormatter) quoted(text []rune, at, base int) int {
	for at < len(text) && text[at] != '"' {
		if text[at] != '\\' {
			f.emit(string(text[at]), base+at)
			at++
			continue
		}
		decoded, end := contentEscape(text, at)
		f.emit(string(decoded), base+at)
		at = end
	}
	return at
}

func contentEscape(text []rune, at int) (rune, int) {
	end := at + 2
	switch text[at+1] {
	case 'n':
		return '\n', end
	case 'r':
		return '\r', end
	case 't':
		return '\t', end
	case 'b':
		return '\b', end
	case 'f':
		return '\f', end
	case 'u':
		value, _ := strconv.ParseUint(string(text[at+2:at+6]), 16, 16)
		end = at + 6
		if value >= 0xd800 && value <= 0xdbff && end+6 <= len(text) && text[end] == '\\' && text[end+1] == 'u' {
			low, _ := strconv.ParseUint(string(text[end+2:end+6]), 16, 16)
			if low >= 0xdc00 && low <= 0xdfff {
				return utf16.DecodeRune(rune(value), rune(low)), end + 6
			}
		}
		if utf16.IsSurrogate(rune(value)) {
			return utf8.RuneError, end
		}
		return rune(value), end
	default:
		return text[at+1], end
	}
}

func (f *contentFormatter) spans(spans []api.RedactedSpan) []api.RedactedSpan {
	out := make([]api.RedactedSpan, 0)
	for _, span := range spans {
		start := -1
		for i, origin := range f.origins {
			inside := origin >= span.Start && origin < span.Start+span.Length
			if inside && start < 0 {
				start = i
			}
			if start >= 0 && (!inside || i == len(f.origins)-1) {
				end := i
				if inside {
					end++
				}
				part := span
				part.Start, part.Length = start, end-start
				out = append(out, part)
				start = -1
			}
		}
	}
	return out
}
