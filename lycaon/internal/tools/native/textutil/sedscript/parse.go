package sedscript

import (
	"context"
	"strconv"
	"strings"
)

// scriptCursor scans tokens, delimited patterns, escapes and literal text
// blocks from a sed script string.
type scriptCursor struct {
	src string
	pos int
}

// parser reads GNU sed: addresses (N, $, /re/ and \cREc with I, first~step,
// 0,/re/, addr,+N, addr,~N), !, {}, labels and every command except e and
// the M regex mode, which run a shell or need a multiline engine.
type parser struct {
	scriptCursor
	ctx      context.Context
	extended bool
	compiler Compiler
	program  *Program
	open     []int
	labels   map[string]int
	branches []int
}

// Parse compiles a complete script. Scripts from several -e and -f sources
// are joined with newlines by the caller.
func Parse(ctx context.Context, script string, extended bool, compiler Compiler) (*Program, error) {
	p := &parser{scriptCursor: scriptCursor{src: script}, ctx: ctx, extended: extended, compiler: compiler, program: &Program{}, labels: map[string]int{}}
	p.program.quiet = script == "#n" || strings.HasPrefix(script, "#n\n")
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
	for _, index := range p.branches {
		c := p.program.commands[index]
		if c.arg == "" {
			c.jump = len(p.program.commands)
			continue
		}
		target, ok := p.labels[c.arg]
		if !ok {
			return nil, p.fail("a :" + c.arg + " label for " + string(c.name))
		}
		c.jump = target
	}
	return p.program, nil
}

func (p *scriptCursor) fail(expected string) error {
	return &ScriptError{Offset: p.pos, Expected: expected}
}

func (p *scriptCursor) unsupported(expected string) error {
	return &ScriptError{Offset: p.pos, Expected: expected, Unsupported: true}
}

func (p *scriptCursor) skip(set string) {
	for p.pos < len(p.src) && strings.IndexByte(set, p.src[p.pos]) >= 0 {
		p.pos++
	}
}

func (p *scriptCursor) peek() byte {
	if p.pos < len(p.src) {
		return p.src[p.pos]
	}
	return 0
}

func (p *parser) command() error {
	c := &command{number: -1}
	if err := p.addresses(c); err != nil {
		return err
	}
	if p.pos >= len(p.src) {
		return p.fail("a command")
	}
	c.name = p.src[p.pos]
	p.pos++
	if c.first != nil && strings.IndexByte(":}#", c.name) >= 0 {
		return p.fail(string(c.name) + " without an address")
	}
	if c.second != nil && (c.name == 'q' || c.name == 'Q') {
		return p.fail(string(c.name) + " with at most one address")
	}
	index := len(p.program.commands)
	switch c.name {
	case '#':
		for p.pos < len(p.src) && p.src[p.pos] != '\n' {
			p.pos++
		}
		return nil
	case '{':
		p.open = append(p.open, index)
		p.program.commands = append(p.program.commands, c)
		return nil
	case '}':
		if len(p.open) == 0 {
			return p.fail("a { before }")
		}
		p.program.commands[p.open[len(p.open)-1]].jump = index + 1
		p.open = p.open[:len(p.open)-1]
	case ':':
		if c.arg = p.label(); c.arg == "" {
			return p.fail("a label after :")
		}
		if _, ok := p.labels[c.arg]; ok {
			return p.fail("a label defined once")
		}
		p.labels[c.arg] = index
		p.program.commands = append(p.program.commands, c)
		return nil
	case 'b', 't', 'T':
		c.arg = p.label()
		p.branches = append(p.branches, index)
		p.program.commands = append(p.program.commands, c)
		return nil
	case '=', 'd', 'D', 'g', 'G', 'h', 'H', 'n', 'N', 'p', 'P', 'x', 'z', 'F':
	case 'l', 'q', 'Q':
		c.number = p.number()
	case 'a', 'i', 'c':
		text, err := p.text()
		if err != nil {
			return err
		}
		c.text = text
		p.program.commands = append(p.program.commands, c)
		return nil
	case 'r', 'R', 'w', 'W':
		if c.arg = p.filename(); c.arg == "" {
			return p.fail("a file name after " + string(c.name))
		}
		p.file(c.name, c.arg)
		p.program.commands = append(p.program.commands, c)
		return nil
	case 's':
		sub, err := p.substitution()
		if err != nil {
			return err
		}
		c.sub = sub
	case 'y':
		translate, err := p.translation()
		if err != nil {
			return err
		}
		c.translate = translate
	case 'v':
		for p.pos < len(p.src) && strings.IndexByte(";\n}", p.src[p.pos]) < 0 {
			p.pos++
		}
	case 'e':
		return p.unsupported("commands other than e, which runs a shell")
	default:
		p.pos--
		return p.fail("a sed command")
	}
	p.program.commands = append(p.program.commands, c)
	return p.end()
}

// addresses reads up to two addresses and any !.
func (p *parser) addresses(c *command) error {
	var err error
	if c.first, err = p.address(true); err != nil {
		return err
	}
	if c.first != nil && p.peek() == ',' {
		p.pos++
		p.skip(" \t")
		if c.second, err = p.address(false); err != nil {
			return err
		}
		if c.second == nil {
			return p.fail("a second address after ,")
		}
	}
	if c.first != nil && c.first.kind == addrZero && (c.second == nil || c.second.kind != addrRegex) {
		return p.fail("line 0 only as 0,/regex/")
	}
	p.skip(" \t")
	if p.peek() == '!' {
		c.negate = true
		p.pos++
		p.skip(" \t")
	}
	return nil
}

// end requires a separator after a command that takes no text.
func (p *scriptCursor) end() error {
	p.skip(" \t")
	switch p.peek() {
	case 0, ';', '\n', '}', '#':
		return nil
	}
	return p.fail("; or a newline after the command")
}

// label reads a label up to a newline or semicolon, without surrounding blanks.
func (p *scriptCursor) label() string {
	p.skip(" \t")
	start := p.pos
	for p.pos < len(p.src) && p.src[p.pos] != '\n' && p.src[p.pos] != ';' {
		p.pos++
	}
	return strings.TrimRight(p.src[start:p.pos], " \t")
}

// filename reads a file name to the end of the line, as GNU sed does.
func (p *scriptCursor) filename() string {
	p.skip(" \t")
	start := p.pos
	for p.pos < len(p.src) && p.src[p.pos] != '\n' {
		p.pos++
	}
	return p.src[start:p.pos]
}

func (p *parser) file(name byte, file string) {
	list := &p.program.writes
	if name == 'r' || name == 'R' {
		list = &p.program.reads
	} else if file == Stdout || file == Stderr {
		return
	}
	for _, existing := range *list {
		if existing == file {
			return
		}
	}
	*list = append(*list, file)
}

// number reads an optional decimal argument (q 5, l 40); -1 when absent.
func (p *scriptCursor) number() int {
	p.skip(" \t")
	start := p.pos
	for p.pos < len(p.src) && p.src[p.pos] >= '0' && p.src[p.pos] <= '9' {
		p.pos++
	}
	if start == p.pos {
		return -1
	}
	n, err := strconv.Atoi(p.src[start:p.pos])
	if err != nil {
		return -1
	}
	return n
}

// text reads a, i and c text in the POSIX form (a\ then lines, a trailing
// backslash continuing) and the GNU one-line form (a text, leading blanks
// removed). A backslash quotes the next byte.
func (p *scriptCursor) text() ([]byte, error) {
	p.skip(" \t")
	switch {
	case strings.HasPrefix(p.src[p.pos:], "\\\n"):
		p.pos += 2
	case p.peek() == '\\':
		p.pos++
	case p.pos >= len(p.src) || p.peek() == '\n':
		return nil, p.fail("text after a, i or c")
	}
	var out []byte
	for p.pos < len(p.src) {
		c := p.src[p.pos]
		p.pos++
		switch {
		case c == '\n':
			return out, nil
		case c == '\\' && p.pos < len(p.src):
			out = append(out, p.src[p.pos])
			p.pos++
		default:
			out = append(out, c)
		}
	}
	return out, nil
}
