package sedscript

import (
	"unicode"
)

// substitute replaces the selected matches: the ordinal-th only, or with g
// every match from the ordinal-th (default first). An empty match directly
// after a previous match is skipped, as POSIX requires, and the search then
// advances one scalar so it always terminates.
func (m *Machine) substitute(s *substitution) error {
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
		if start == end && start == previousEnd {
			from = start + scalarWidth(space, start)
			continue
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
	m.space.pattern = append(out, space[copied:]...)
	m.stream.replaced = true
	if err := m.account(); err != nil {
		return err
	}
	if s.print {
		if err := m.emit(m.stream.out, m.space.pattern, m.stream.terminated); err != nil {
			return err
		}
	}
	if s.file != "" {
		return m.emit(m.outputs[s.file], m.space.pattern, m.stream.terminated)
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
