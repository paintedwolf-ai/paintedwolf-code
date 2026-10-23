package sedscript

import "strconv"

// address reads one address. Line 0, first~step and the second-address
// forms +N and ~N follow GNU sed.
func (p *parser) address(first bool) (*address, error) {
	switch c := p.peek(); {
	case c == '$':
		p.pos++
		return &address{kind: addrLast}, nil
	case c >= '0' && c <= '9':
		line := p.number()
		if p.peek() == '~' && first {
			p.pos++
			return &address{kind: addrStep, line: line, step: max(p.number(), 0)}, nil
		}
		if line == 0 {
			if !first {
				return nil, p.fail("a line number from 1")
			}
			return &address{kind: addrZero}, nil
		}
		return &address{kind: addrLine, line: line}, nil
	case (c == '+' || c == '~') && !first:
		p.pos++
		n := p.number()
		if n < 0 {
			return nil, p.fail("a count after " + string(c))
		}
		if c == '+' {
			return &address{kind: addrRelative, line: n}, nil
		}
		return &address{kind: addrMultiple, step: n}, nil
	case c == '/':
		p.pos++
		return p.regexAddress('/')
	case c == '\\':
		p.pos++
		delimiter := p.peek()
		if delimiter == 0 || delimiter == '\n' || delimiter == '\\' {
			return nil, p.fail("a regex delimiter after \\")
		}
		p.pos++
		return p.regexAddress(delimiter)
	}
	return nil, nil
}

func (p *parser) regexAddress(delimiter byte) (*address, error) {
	pattern, err := p.delimited(delimiter)
	if err != nil {
		return nil, err
	}
	fold := false
	for p.peek() == 'I' {
		fold = true
		p.pos++
	}
	if p.peek() == 'M' {
		return nil, p.unsupported("the I address flag; multiline mode M is not supported")
	}
	regex, err := p.compile(pattern, fold)
	if err != nil {
		return nil, err
	}
	return &address{kind: addrRegex, regex: regex}, nil
}

// compile returns nil for the empty regex, which reuses the last one applied.
func (p *parser) compile(pattern string, fold bool) (Regex, error) {
	if pattern == "" {
		return nil, nil
	}
	regex, err := p.compiler.Compile(p.ctx, pattern, p.extended, fold)
	if err != nil {
		return nil, &RegexError{Offset: p.pos, Cause: err}
	}
	return regex, nil
}

// delimited reads up to an unescaped delimiter. An escaped delimiter is the
// literal delimiter and \n is a newline; other escapes stay for the regex
// grammar.
func (p *scriptCursor) delimited(delimiter byte) (string, error) {
	var out []byte
	for p.pos < len(p.src) {
		c := p.src[p.pos]
		switch {
		case c == delimiter:
			p.pos++
			return string(out), nil
		case c == '\\' && p.pos+1 < len(p.src):
			switch next := p.src[p.pos+1]; next {
			case delimiter:
				out = append(out, delimiter)
			case '\n':
				out = append(out, '\\', 'n')
			default:
				out = append(out, c, next)
			}
			p.pos += 2
		case c == '\n':
			return "", p.fail("a terminating " + string(delimiter))
		default:
			out = append(out, c)
			p.pos++
		}
	}
	return "", p.fail("a terminating " + string(delimiter))
}

func (p *parser) substitution() (*substitution, error) {
	delimiter := p.peek()
	if delimiter == 0 || delimiter == '\n' || delimiter == '\\' {
		return nil, p.fail("an s delimiter other than newline or backslash")
	}
	p.pos++
	pattern, err := p.delimited(delimiter)
	if err != nil {
		return nil, err
	}
	sub := &substitution{}
	if err := p.replacement(sub, delimiter); err != nil {
		return nil, err
	}
	fold, err := p.flags(sub)
	if err != nil {
		return nil, err
	}
	if sub.regex, err = p.compile(pattern, fold); err != nil {
		return nil, err
	}
	if sub.regex != nil && sub.maxGroup > sub.regex.Groups() {
		return nil, p.fail("a capture group the regex defines")
	}
	return sub, nil
}

// flags reads g, p, N, I and w FILE; e and M refuse.
func (p *parser) flags(sub *substitution) (fold bool, err error) {
	for p.pos < len(p.src) {
		switch c := p.src[p.pos]; {
		case c == 'g' && !sub.global:
			sub.global = true
		case c == 'p' && !sub.print:
			sub.print = true
		case (c == 'I' || c == 'i') && !fold:
			fold = true
		case c >= '1' && c <= '9' && sub.ordinal == 0:
			sub.ordinal = p.number()
			if sub.ordinal < 1 {
				return false, p.fail("a match ordinal")
			}
			continue
		case c == 'w':
			p.pos++
			if sub.file = p.filename(); sub.file == "" {
				return false, p.fail("a file name after the w flag")
			}
			p.file('w', sub.file)
			return fold, nil
		case c == 'e':
			return false, p.unsupported("s flags other than e, which runs a shell")
		case c == 'M' || c == 'm':
			return false, p.unsupported("s flags g, p, N, I and w; multiline mode M is not supported")
		default:
			return fold, nil
		}
		p.pos++
	}
	return fold, nil
}

// replacement reads & and \0..\9 (the match and groups), \L \U \E \l \u case
// conversion, \n \t \r \f \v \a, \cX, \dNNN, \oNNN, \xHH and escaped bytes.
func (p *scriptCursor) replacement(sub *substitution, delimiter byte) error {
	var literal []byte
	flush := func() {
		if len(literal) > 0 {
			sub.pieces = append(sub.pieces, piece{kind: pieceLiteral, literal: literal})
			literal = nil
		}
	}
	add := func(next piece) {
		flush()
		sub.pieces = append(sub.pieces, next)
	}
	for p.pos < len(p.src) {
		c := p.src[p.pos]
		p.pos++
		switch {
		case c == delimiter:
			flush()
			return nil
		case c == '&':
			add(piece{kind: pieceGroup})
		case c == '\n':
			return p.fail("a terminating " + string(delimiter) + "; escape newlines in the replacement")
		case c == '\\' && p.pos < len(p.src):
			e := p.src[p.pos]
			p.pos++
			if kind, ok := caseEscapes[e]; ok {
				add(piece{kind: kind})
				continue
			}
			if e >= '0' && e <= '9' {
				add(piece{kind: pieceGroup, group: int(e - '0')})
				sub.maxGroup = max(sub.maxGroup, int(e-'0'))
				continue
			}
			literal = append(literal, p.escape(e)...)
		default:
			literal = append(literal, c)
		}
	}
	return p.fail("a terminating " + string(delimiter))
}

var caseEscapes = map[byte]pieceKind{'U': pieceUpper, 'L': pieceLower, 'E': pieceEnd, 'u': pieceOneUpper, 'l': pieceOneLower}

// escape decodes the byte after a backslash in replacement and y text; an
// unknown escape is the byte itself.
func (p *scriptCursor) escape(e byte) []byte {
	if b, ok := controlEscapes[e]; ok {
		return []byte{b}
	}
	switch e {
	case 'c':
		if p.pos < len(p.src) {
			x := p.src[p.pos]
			p.pos++
			if x >= 'a' && x <= 'z' {
				x -= 'a' - 'A'
			}
			return []byte{x ^ 0x40}
		}
	case 'd', 'o', 'x':
		base, width := 10, 3
		switch e {
		case 'o':
			base = 8
		case 'x':
			base, width = 16, 2
		}
		end := p.pos
		for end < len(p.src) && end-p.pos < width && digitValue(p.src[end]) < base {
			end++
		}
		if value, err := strconv.ParseUint(p.src[p.pos:end], base, 8); err == nil && end > p.pos {
			p.pos = end
			return []byte{byte(value)}
		}
	}
	return []byte{e}
}

var controlEscapes = map[byte]byte{'n': '\n', 't': '\t', 'r': '\r', 'f': '\f', 'v': '\v', 'a': '\a'}

func digitValue(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10
	case c >= 'A' && c <= 'F':
		return int(c-'A') + 10
	}
	return 99
}
