package textfile

import (
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// EncodedSize validates text and measures its exact selected representation
// without allocating the representation. Callers can admit capacity first.
func EncodedSize(content, encoding string) (int64, error) {
	if !utf8.ValidString(content) || strings.IndexByte(content, 0) >= 0 {
		return 0, ErrBinary
	}
	size, ok := encodedSize(content, encoding)
	if !ok {
		return 0, ErrUnsupported
	}
	return size, nil
}

func encodedSize(content, encoding string) (int64, bool) {
	switch encoding {
	case UTF8:
		return int64(len(content)), true
	case UTF8BOM:
		return int64(len(content)) + 3, true
	case UTF16LE, UTF16BE, UTF16LEBOM, UTF16BEBOM:
		var size int64
		if encoding == UTF16LEBOM || encoding == UTF16BEBOM {
			size = 2
		}
		for _, r := range content {
			if r > 0xffff {
				size += 4
			} else {
				size += 2
			}
		}
		return size, true
	default:
		return 0, false
	}
}

// EncodeBounded rejects oversized output before allocating encoded bytes.
func EncodeBounded(content, encoding string, limits Limits) ([]byte, error) {
	if limits.MaxRawBytes <= 0 || limits.MaxTextBytes <= 0 {
		return nil, ErrLimitsInvalid
	}
	size, err := EncodedSize(content, encoding)
	if err != nil {
		return nil, err
	}
	if int64(len(content)) > limits.MaxTextBytes {
		return nil, ErrTextTooLarge
	}
	if size > limits.MaxRawBytes {
		return nil, ErrRawTooLarge
	}
	encoded, _ := encodeUnchecked(content, encoding)
	return encoded, nil
}

// encodeUnchecked also creates deliberately invalid fixtures for the codec's
// classifier tests; production calls validate through EncodeBounded first.
func encodeUnchecked(content, encoding string) ([]byte, bool) {
	switch encoding {
	case UTF8:
		return []byte(content), true
	case UTF8BOM:
		out := make([]byte, len(bomUTF8)+len(content))
		copy(out, bomUTF8)
		copy(out[len(bomUTF8):], content)
		return out, true
	case UTF16LE:
		return encodeUTF16(content, false, false), true
	case UTF16LEBOM:
		return encodeUTF16(content, false, true), true
	case UTF16BE:
		return encodeUTF16(content, true, false), true
	case UTF16BEBOM:
		return encodeUTF16(content, true, true), true
	default:
		return nil, false
	}
}

// encodeUTF16 writes directly into its exact byte capacity. It retains no
// intermediate rune or code-unit array.
func encodeUTF16(content string, bigEndian, bom bool) []byte {
	encoding := UTF16LE
	if bom {
		encoding = UTF16LEBOM
	}
	size, _ := encodedSize(content, encoding)
	out := make([]byte, 0, int(size))
	if bom {
		if bigEndian {
			out = append(out, bomUTF16BE...)
		} else {
			out = append(out, bomUTF16LE...)
		}
	}
	unit := func(value uint16) {
		if bigEndian {
			out = append(out, byte(value>>8), byte(value&0xff))
		} else {
			out = append(out, byte(value&0xff), byte(value>>8))
		}
	}
	for _, r := range content {
		if r <= 0xffff {
			unit(uint16(r & 0xffff))
		} else {
			first, second := utf16.EncodeRune(r)
			unit(uint16(first & 0xffff))
			unit(uint16(second & 0xffff))
		}
	}
	return out
}

// SerializeEOL writes LF-normalized text with its recorded line ending, "lf"
// or "crlf".
func SerializeEOL(content, eol string) string {
	content = strings.ReplaceAll(content, "\r\n", "\n")
	if eol == "crlf" {
		return strings.ReplaceAll(content, "\n", "\r\n")
	}
	return content
}

// HasMixedEOL reports whether content contains both CRLF and bare LF line endings.
func HasMixedEOL(content string) bool {
	crlf := strings.Count(content, "\r\n")
	lf := strings.Count(content, "\n") - crlf
	return crlf > 0 && lf > 0
}

type rawLine struct {
	body string
	eol  string
}

func splitRawLines(s string) []rawLine {
	if s == "" {
		return nil
	}
	var out []rawLine
	for len(s) > 0 {
		idx := strings.IndexByte(s, '\n')
		if idx < 0 {
			out = append(out, rawLine{body: s, eol: ""})
			break
		}
		if idx > 0 && s[idx-1] == '\r' {
			out = append(out, rawLine{body: s[:idx-1], eol: "\r\n"})
		} else {
			out = append(out, rawLine{body: s[:idx], eol: "\n"})
		}
		s = s[idx+1:]
	}
	return out
}

// SerializePreservingMixedEOL serializes LF-normalized draft text while keeping
// the exact original per-line terminators ("\r\n" vs "\n") of untouched lines
// from beforeRawText. Modified or newly inserted lines take fallbackEOL.
func SerializePreservingMixedEOL(beforeRawText, draft, fallbackEOL string) (string, bool) {
	if beforeRawText == "" || !HasMixedEOL(beforeRawText) {
		serialized := SerializeEOL(draft, fallbackEOL)
		return serialized, false
	}
	beforeLines := splitRawLines(beforeRawText)
	draftLines := splitRawLines(strings.ReplaceAll(draft, "\r\n", "\n"))
	if len(draftLines) == 0 {
		return "", false
	}
	fallbackTerm := "\n"
	if fallbackEOL == "crlf" {
		fallbackTerm = "\r\n"
	}

	// Match common unchanged prefix and suffix lines by exact body.
	prefix := 0
	for prefix < len(beforeLines) && prefix < len(draftLines) && beforeLines[prefix].body == draftLines[prefix].body {
		prefix++
	}
	beforeEnd := len(beforeLines)
	draftEnd := len(draftLines)
	for beforeEnd > prefix && draftEnd > prefix && beforeLines[beforeEnd-1].body == draftLines[draftEnd-1].body {
		beforeEnd--
		draftEnd--
	}

	// Map each draft line index in [prefix, draftEnd) to an original before line
	// when unchanged interior lines survive between multiple edit hunks.
	midMatch := make([]int, draftEnd-prefix)
	for i := range midMatch {
		midMatch[i] = -1
	}
	cursor := prefix
	for j := prefix; j < draftEnd; j++ {
		for k := cursor; k < beforeEnd; k++ {
			if beforeLines[k].body == draftLines[j].body {
				midMatch[j-prefix] = k
				cursor = k + 1
				break
			}
		}
	}

	var b strings.Builder
	b.Grow(len(draft) + len(draftLines))
	for j, dl := range draftLines {
		b.WriteString(dl.body)
		if dl.eol == "" {
			continue
		}
		origIdx := -1
		switch {
		case j < prefix:
			origIdx = j
		case j >= draftEnd:
			origIdx = beforeEnd + (j - draftEnd)
		default:
			origIdx = midMatch[j-prefix]
		}
		if origIdx >= 0 && origIdx < len(beforeLines) && beforeLines[origIdx].eol != "" {
			b.WriteString(beforeLines[origIdx].eol)
		} else {
			b.WriteString(fallbackTerm)
		}
	}
	serialized := b.String()
	return serialized, HasMixedEOL(serialized)
}

// EncodeDocument returns the bytes a save of LF-normalized text writes in its
// recorded line ending and encoding.
func EncodeDocument(content, eol, encoding string, limits Limits) ([]byte, error) {
	return EncodeBounded(SerializeEOL(content, eol), encoding, limits)
}
