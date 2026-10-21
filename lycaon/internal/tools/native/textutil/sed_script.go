package textutil

import (
	"context"
	"strconv"
	"strings"
)

// PosixRegex is one compiled BRE or ERE with POSIX leftmost-longest matching
// and POSIX subexpression offsets.
type PosixRegex interface {
	// Find returns the leftmost-longest match starting at or after from as
	// [start, end, group1 start, group1 end, ...] (-1 when a group is unset),
	// or nil. Anchors and assertions see the whole subject.
	Find(ctx context.Context, subject []byte, from int) ([]int, error)
	// Groups is the number of capture groups a replacement may name.
	Groups() int
}

// PosixRegexEngine compiles sed's regular expressions. It must be the shared
// POSIX BRE/ERE engine; sed never runs on RE2.
type PosixRegexEngine interface {
	Compile(ctx context.Context, pattern string, extended bool) (PosixRegex, error)
}

// sedAddress is a line number, the last line ($) or a regex.
type sedAddress struct {
	line  int
	last  bool
	regex PosixRegex
}

// sedCommand is one parsed command. block holds the index just past the
// matching } for {; its address skips the block when it does not select.
type sedCommand struct {
	first, second *sedAddress
	negate        bool
	name          byte
	text          string
	sub           *sedSubstitution
	block         int
	// range state of a two-address command
	active bool
	end    int
}

type sedSubstitution struct {
	regex       PosixRegex
	replacement []sedPiece
	global      bool
	print       bool
	ordinal     int
}

// sedPiece is literal text, the whole match (group 0) or a capture group.
type sedPiece struct {
	literal string
	group   int
}

type sedScriptError struct {
	offset      int
	expected    string
	unsupported bool
}

func (e *sedScriptError) Error() string {
	return e.expected + " at script byte " + strconv.Itoa(e.offset)
}

// sedParser reads the catalog's sed grammar: addresses, !, {}, p d q a i c s.
// Anything else, including w, r, e and s///w or s///e, refuses before any
// input is read.
type sedParser struct {
	ctx      context.Context
	src      string
	pos      int
	extended bool
	engine   PosixRegexEngine
	commands []*sedCommand
	open     []int
}

func parseSed(ctx context.Context, src string, extended bool, engine PosixRegexEngine) ([]*sedCommand, error) {
	p := &sedParser{ctx: ctx, src: src, extended: extended, engine: engine}
	for {
		p.skip(" \t\n;")
		if p.pos >= len(p.src) {
			break
		}
		if err := p.command(); err != nil {
			return nil, err
		}
	}
	if len(p.open) > 0 {
		return nil, p.fail("a } closing every {")
	}
	return p.commands, nil
}

func (p *sedParser) fail(expected string) error {
	return &sedScriptError{offset: p.pos, expected: expected}
}

func (p *sedParser) unsupported(expected string) error {
	return &sedScriptError{offset: p.pos, expected: expected, unsupported: true}
}

func (p *sedParser) skip(set string) {
	for p.pos < len(p.src) && strings.IndexByte(set, p.src[p.pos]) >= 0 {
		p.pos++
	}
}

func (p *sedParser) peek() byte {
	if p.pos < len(p.src) {
		return p.src[p.pos]
	}
	return 0
}

func (p *sedParser) command() error {
	c := &sedCommand{}
	var err error
	if c.first, err = p.address(); err != nil {
		return err
	}
	if c.first != nil && p.peek() == ',' {
		p.pos++
		p.skip(" \t")
		if c.second, err = p.address(); err != nil {
			return err
		}
		if c.second == nil {
			return p.fail("a second address after ,")
		}
	}
	p.skip(" \t")
	if p.peek() == '!' {
		c.negate = true
		p.pos++
		p.skip(" \t")
	}
	if p.pos >= len(p.src) {
		return p.fail("a command")
	}
	c.name = p.src[p.pos]
	p.pos++
	switch c.name {
	case '{':
		p.open = append(p.open, len(p.commands))
	case '}':
		if c.first != nil || c.negate {
			return p.fail("} without an address")
		}
		if len(p.open) == 0 {
			return p.fail("a { before }")
		}
		start := p.open[len(p.open)-1]
		p.open = p.open[:len(p.open)-1]
		p.commands[start].block = len(p.commands) + 1
	case 'p', 'd':
	case 'q':
		if c.second != nil {
			return p.fail("q with at most one address")
		}
	case 'a', 'i', 'c':
		if c.name != 'c' && c.second != nil {
			return p.fail(string(c.name) + " with at most one address")
		}
		c.text = p.text()
	case 's':
		if c.sub, err = p.substitution(); err != nil {
			return err
		}
	default:
		return p.unsupported("the catalog's p, d, q, a, i, c, s, { and } commands")
	}
	p.commands = append(p.commands, c)
	return p.end(c.name)
}

// end requires a command separator after commands that take no text.
func (p *sedParser) end(name byte) error {
	if name == 'a' || name == 'i' || name == 'c' || name == '{' {
		return nil
	}
	p.skip(" \t")
	switch p.peek() {
	case 0, ';', '\n', '}':
		return nil
	}
	if name == 'q' && p.peek() >= '0' && p.peek() <= '9' {
		return p.unsupported("q without an exit code")
	}
	return p.fail("; or a newline after the command")
}

func (p *sedParser) address() (*sedAddress, error) {
	switch c := p.peek(); {
	case c == '$':
		p.pos++
		return &sedAddress{last: true}, nil
	case c >= '0' && c <= '9':
		start := p.pos
		for p.pos < len(p.src) && p.src[p.pos] >= '0' && p.src[p.pos] <= '9' {
			p.pos++
		}
		line, err := strconv.Atoi(p.src[start:p.pos])
		if err != nil || line < 1 {
			p.pos = start
			return nil, p.fail("a line number from 1")
		}
		if next := p.peek(); next == '~' || next == '+' {
			return nil, p.unsupported("line, $ or /regex/ addresses")
		}
		return &sedAddress{line: line}, nil
	case c == '/':
		p.pos++
		pattern, err := p.delimited('/')
		if err != nil {
			return nil, err
		}
		regex, err := p.compile(pattern)
		if err != nil {
			return nil, err
		}
		return &sedAddress{regex: regex}, nil
	case c == '\\':
		return nil, p.unsupported("/regex/ addresses")
	}
	return nil, nil
}

func (p *sedParser) compile(pattern string) (PosixRegex, error) {
	if pattern == "" {
		return nil, p.unsupported("a nonempty regex; the last-regex shorthand is not supported")
	}
	if strings.IndexByte(pattern, '\n') >= 0 {
		return nil, p.fail("a regex without a newline")
	}
	regex, err := p.engine.Compile(p.ctx, pattern, p.extended)
	if err != nil {
		return nil, &sedRegexError{offset: p.pos, cause: err}
	}
	return regex, nil
}

// sedRegexError is the shared engine's refusal of one script regex.
type sedRegexError struct {
	offset int
	cause  error
}

func (e *sedRegexError) Error() string { return e.cause.Error() }
func (e *sedRegexError) Unwrap() error { return e.cause }

// delimited reads up to an unescaped delimiter; an escaped delimiter is the
// literal delimiter, other escapes are kept for the regex grammar.
func (p *sedParser) delimited(delimiter byte) (string, error) {
	var out strings.Builder
	for p.pos < len(p.src) {
		c := p.src[p.pos]
		switch {
		case c == delimiter:
			p.pos++
			return out.String(), nil
		case c == '\\' && p.pos+1 < len(p.src):
			if p.src[p.pos+1] == delimiter {
				out.WriteByte(delimiter)
			} else {
				out.WriteString(p.src[p.pos : p.pos+2])
			}
			p.pos += 2
		case c == '\n':
			return "", p.fail("a terminating " + string(delimiter))
		default:
			out.WriteByte(c)
			p.pos++
		}
	}
	return "", p.fail("a terminating " + string(delimiter))
}

// text reads a, i and c text in the POSIX/BSD form (a\ then lines, a
// trailing backslash continuing) and the GNU one-line form (a text). A
// backslash quotes the next byte.
func (p *sedParser) text() string {
	p.skip(" \t")
	if strings.HasPrefix(p.src[p.pos:], "\\\n") {
		p.pos += 2
	} else if p.peek() == '\\' {
		p.pos++
	}
	var out strings.Builder
	for p.pos < len(p.src) {
		c := p.src[p.pos]
		p.pos++
		switch {
		case c == '\n':
			return out.String()
		case c == '\\' && p.pos < len(p.src):
			out.WriteByte(p.src[p.pos])
			p.pos++
		default:
			out.WriteByte(c)
		}
	}
	return out.String()
}

func (p *sedParser) substitution() (*sedSubstitution, error) {
	delimiter := p.peek()
	if delimiter == 0 || delimiter == '\n' || delimiter == '\\' {
		return nil, p.fail("an s delimiter other than newline or backslash")
	}
	p.pos++
	pattern, err := p.delimited(delimiter)
	if err != nil {
		return nil, err
	}
	regex, err := p.compile(pattern)
	if err != nil {
		return nil, err
	}
	sub := &sedSubstitution{regex: regex}
	if sub.replacement, err = p.replacement(delimiter, regex.Groups()); err != nil {
		return nil, err
	}
	for p.pos < len(p.src) {
		switch c := p.src[p.pos]; {
		case c == 'g' && !sub.global:
			sub.global = true
		case c == 'p' && !sub.print:
			sub.print = true
		case c >= '1' && c <= '9' && sub.ordinal == 0:
			start := p.pos
			for p.pos+1 < len(p.src) && p.src[p.pos+1] >= '0' && p.src[p.pos+1] <= '9' {
				p.pos++
			}
			sub.ordinal, _ = strconv.Atoi(p.src[start : p.pos+1])
		case c == 'w' || c == 'W' || c == 'e' || c == 'I' || c == 'i' || c == 'M' || c == 'm':
			return nil, p.unsupported("s flags g, p and a match ordinal")
		default:
			return sub, nil
		}
		p.pos++
	}
	return sub, nil
}

// replacement reads & (the match), \1..\9, \n (newline), escaped bytes and
// literal text up to the delimiter. A group beyond the regex refuses.
func (p *sedParser) replacement(delimiter byte, groups int) ([]sedPiece, error) {
	var pieces []sedPiece
	var literal strings.Builder
	flush := func() {
		if literal.Len() > 0 {
			pieces = append(pieces, sedPiece{literal: literal.String(), group: -1})
			literal.Reset()
		}
	}
	for p.pos < len(p.src) {
		c := p.src[p.pos]
		p.pos++
		switch {
		case c == delimiter:
			flush()
			return pieces, nil
		case c == '&':
			flush()
			pieces = append(pieces, sedPiece{group: 0})
		case c == '\\' && p.pos < len(p.src):
			e := p.src[p.pos]
			p.pos++
			switch {
			case e >= '1' && e <= '9':
				if int(e-'0') > groups {
					return nil, p.fail("a capture group the regex defines")
				}
				flush()
				pieces = append(pieces, sedPiece{group: int(e - '0')})
			case e == 'n':
				literal.WriteByte('\n')
			default:
				literal.WriteByte(e)
			}
		default:
			literal.WriteByte(c)
		}
	}
	return nil, p.fail("a terminating " + string(delimiter))
}
