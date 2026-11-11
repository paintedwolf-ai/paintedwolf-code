package commandsurface

import (
	"bytes"
	"strings"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/toolcontract"
)

func (d *SyntaxDiagnostic) captureContext(source []byte) {
	if d.Parser == nil || !utf8.Valid(source) {
		return
	}
	at := min(max(d.Span.StartByte, 0), len(source))
	start := bytes.LastIndexByte(source[:at], '\n') + 1
	end := len(source)
	if next := bytes.IndexByte(source[at:], '\n'); next >= 0 {
		end = at + next
	}
	start = max(start, at-80)
	end = min(end, start+160)
	for start < len(source) && !utf8.RuneStart(source[start]) {
		start++
	}
	for end > start && end < len(source) && !utf8.RuneStart(source[end]) {
		end--
	}
	if end <= start {
		return
	}
	before := source[:start]
	d.Parser.Excerpt = strings.Clone(string(source[start:end]))
	d.Parser.ExcerptAt = toolcontract.SourceSpan{StartByte: start, EndByte: end, Line: bytes.Count(before, []byte{'\n'}) + 1, Column: start - bytes.LastIndexByte(before, '\n')}
}
