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
	m.space.scratch = m.space.scratch[:0]
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
		empty := start == end
		if empty && start == previousEnd {
			from = start + scalarWidth(space, start)
			continue
		}
		count++
		if count >= ordinal && (s.global || count == ordinal) {
			if err := m.space.append(&m.space.scratch, space[copied:start]); err != nil {
				return err
			}
			if err := m.space.appendReplacement(s.pieces, space, match); err != nil {
				return err
			}
			copied, replaced = end, true
			if !s.global {
				break
			}
		}
		previousEnd = end
		from = end
		if empty {
			from = end + scalarWidth(space, end)
		}
	}
	if !replaced {
		return nil
	}
	if err := m.space.append(&m.space.scratch, space[copied:]); err != nil {
		return err
	}
	m.space.swapScratch()
	m.stream.pin = nil
	m.stream.replaced = true
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
func (s *spaces) appendReplacement(pieces []piece, space []byte, match []int) error {
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
			if err := s.append(&s.scratch, text); err != nil {
				return err
			}
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
			if err := s.appendRune(r); err != nil {
				return err
			}
			text = text[size:]
		}
	}
	return nil
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
func (p *parser) translation() (map[rune]rune, error) {
	delimiter := p.peek()
	if delimiter == 0 || delimiter == '\n' || delimiter == '\\' {
		return nil, p.fail("a y delimiter other than newline or backslash")
	}
	p.pos++
	var lists [2][]rune
	var err error
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
					if text, err = appendAdmittedBytes(p, text, e); err != nil {
						return nil, err
					}
				} else if b, ok := controlEscapes[e]; ok {
					if text, err = appendAdmittedBytes(p, text, b); err != nil {
						return nil, err
					}
				} else {
					return nil, p.fail("y escapes \\\\, \\n, \\t or the delimiter")
				}
				continue
			}
			if text, err = appendAdmittedBytes(p, text, c); err != nil {
				return nil, err
			}
		}
		for len(text) > 0 {
			r, size := decodeScalar(text)
			if lists[i], err = appendAdmitted(p, lists[i], r, runeElemBytes); err != nil {
				return nil, err
			}
			text = text[size:]
		}
	}
	if len(lists[0]) != len(lists[1]) {
		return nil, p.fail("y source and destination of equal length")
	}
	if err := p.reserve(int64(max(1, len(lists[0]))) * trMapEntryBytes); err != nil {
		return nil, err
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

func (s *spaces) appendRune(r rune) error {
	var encoded [utf8.UTFMax]byte
	var text []byte
	if r >= 0xDC80 && r <= 0xDCFF {
		encoded[0] = byte(r - 0xDC00)
		text = encoded[:1]
	} else {
		text = utf8.AppendRune(encoded[:0], r)
	}
	return s.append(&s.scratch, text)
}
