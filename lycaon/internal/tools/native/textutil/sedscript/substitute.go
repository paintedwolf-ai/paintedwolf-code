package sedscript

import (
	"unicode"
	"unicode/utf8"
)

// apply replaces the selected matches: the ordinal-th only, or with g
// every match from the ordinal-th (default first). An empty match directly
// after a previous match is skipped, as POSIX requires, and the search then
// advances one scalar so it always terminates.
func (s *substitution) apply(m *Machine) error {
	regex, err := m.regex(s.regex)
	if err != nil {
		return err
	}
	if s.maxGroup > regex.Groups() {
		return &RuntimeError{Reason: "a replacement group the regex does not define"}
	}
	space := m.space.pattern
	ordinal := max(s.ordinal, 1)
	var out []byte
	copied, from, count, previousEnd := 0, 0, 0, -1
	replaced := false
	for from <= len(space) {
		match, err := regex.Find(m.ctx, space, from)
		if err != nil {
			return err
		}
		if match == nil {
			break
		}
		start, end := match[0], match[1]
		if start == end {
			if end == previousEnd {
				from = start + scalarWidth(space, start)
				continue
			}
		}
		count++
		if count >= ordinal && (s.global || count == ordinal) {
			out = append(out, space[copied:start]...)
			out = appendReplacement(out, s.pieces, space, match)
			copied, replaced = end, true
			if limit := m.config.SpaceLimit; limit > 0 && int64(len(out)) > limit {
				return &LimitError{Resource: "record_bytes", Maximum: limit, Observed: int64(len(out))}
			}
			if !s.global {
				break
			}
		}
		previousEnd = end
		from = end
		if end == start {
			from = end + scalarWidth(space, end)
		}
	}
	if !replaced {
		return nil
	}
	out = append(out, space[copied:]...)
	m.space.pattern = out
	m.stream.replaced = true
	if err := m.space.account(m.config.SpaceLimit, m.config.Reserve); err != nil {
		return err
	}
	if s.print {
		if err := m.stream.out.emit(m.space.pattern, m.config.Delimiter, m.stream.terminated); err != nil {
			return err
		}
	}
	if s.file != "" {
		return m.outputs[s.file].emit(m.space.pattern, m.config.Delimiter, m.stream.terminated)
	}
	return nil
}

// appendReplacement expands the pieces for one match; \U \L \E set a case
// mode and \u \l convert only the next scalar, across literal and group text.
func appendReplacement(out []byte, pieces []piece, space []byte, match []int) []byte {
	var mode, once pieceKind
	for _, p := range pieces {
		var text []byte
		switch p.kind {
		case pieceLiteral:
			text = p.literal
		case pieceGroup:
			if start, end := match[2*p.group], match[2*p.group+1]; start >= 0 && end >= start {
				text = space[start:end]
			}
		case pieceUpper, pieceLower:
			mode, once = p.kind, 0
			continue
		case pieceEnd:
			mode, once = 0, 0
			continue
		default:
			once = p.kind
			continue
		}
		if mode == 0 && once == 0 {
			out = append(out, text...)
			continue
		}
		for len(text) > 0 {
			r, size := decodeScalar(text)
			switch {
			case once == pieceOneUpper || (once == 0 && mode == pieceUpper):
				r = unicode.ToUpper(r)
			case once == pieceOneLower || (once == 0 && mode == pieceLower):
				r = unicode.ToLower(r)
			}
			once = 0
			out = appendScalar(out, r)
			text = text[size:]
		}
	}
	return out
}

func scalarWidth(b []byte, at int) int {
	if at >= len(b) {
		return 1
	}
	_, size := decodeScalar(b[at:])
	return size
}

// translation reads y/SOURCE/DEST/: equal-length scalar lists where \\, the
// escaped delimiter and the control escapes stand for one scalar.
func (p *scriptCursor) translation() (map[rune]rune, error) {
	delimiter := p.peek()
	if delimiter == 0 || delimiter == '\n' || delimiter == '\\' {
		return nil, p.fail("a y delimiter other than newline or backslash")
	}
	p.pos++
	var lists [2][]rune
	for i := range lists {
		var text []byte
		for {
			if p.pos >= len(p.src) || p.src[p.pos] == '\n' {
				return nil, p.fail("a terminating " + string(delimiter))
			}
			c := p.src[p.pos]
			p.pos++
			if c == delimiter {
				break
			}
			if c == '\\' && p.pos < len(p.src) {
				e := p.src[p.pos]
				p.pos++
				if e == delimiter || e == '\\' {
					text = append(text, e)
				} else if b, ok := controlEscapes[e]; ok {
					text = append(text, b)
				} else {
					return nil, p.fail("y escapes \\\\, \\n, \\t or the delimiter")
				}
				continue
			}
			text = append(text, c)
		}
		for len(text) > 0 {
			r, size := decodeScalar(text)
			lists[i] = append(lists[i], r)
			text = text[size:]
		}
	}
	if len(lists[0]) != len(lists[1]) {
		return nil, p.fail("y source and destination of equal length")
	}
	out := make(map[rune]rune, len(lists[0]))
	for i, r := range lists[0] {
		if _, ok := out[r]; !ok {
			out[r] = lists[1][i]
		}
	}
	return out, nil
}

// decodeScalar decodes one UTF-8 scalar; an invalid byte is its own scalar
// in the surrogate range, so y and case conversion keep it exactly.
func decodeScalar(b []byte) (rune, int) {
	r, size := utf8.DecodeRune(b)
	if r == utf8.RuneError && size == 1 {
		return rune(0xDC00 + int(b[0])), 1
	}
	return r, size
}

func appendScalar(out []byte, r rune) []byte {
	if r >= 0xDC80 && r <= 0xDCFF {
		return append(out, byte(r-0xDC00))
	}
	return utf8.AppendRune(out, r)
}
