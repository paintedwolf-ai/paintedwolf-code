package sedscript

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/toolcontract"
)

const (
	parserBaseBytes         = 256
	commandStructBytes      = 128
	addressStructBytes      = 64
	substitutionStructBytes = 96
	compiledRegexBytes      = 128
	pointerElemBytes        = 8
	intElemBytes            = 8
	stringElemBytes         = 16
	pieceElemBytes          = 32
	runeElemBytes           = 4
	labelMapEntryBytes      = 64
	trMapEntryBytes         = 32
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
	memory   *toolcontract.ProgramMemory
	extended bool
	compiler Compiler
	program  *Program
	open     []int
	labels   map[string]int
	branches []int
}

// Parse compiles a complete script and admits its parsed AST, slices, maps
// and literal/delimited buffers against memory before allocation. Scripts from
// several -e and -f sources are joined with newlines by the caller.
func Parse(ctx context.Context, memory *toolcontract.ProgramMemory, script string, extended bool, compiler Compiler) (*Program, error) {
	if memory == nil {
		return nil, errors.New("sed script parse requires program memory admission")
	}
	baseRelease, err := memory.Reserve(ctx, parserBaseBytes)
	if err != nil {
		return nil, err
	}
	p := &parser{
		scriptCursor: scriptCursor{src: script},
		ctx:          ctx,
		memory:       memory,
		extended:     extended,
		compiler:     compiler,
		program:      &Program{releases: []func(){baseRelease}},
		labels:       map[string]int{},
	}
	if err := p.parseAll(script); err != nil {
		p.program.Close()
		return nil, err
	}
	return p.program, nil
}

func (p *parser) parseAll(script string) error {
	p.program.quiet = script == "#n" || strings.HasPrefix(script, "#n\n")
	for {
		p.skip(" \t\n;")
		if p.pos >= len(p.src) {
			break
		}
		if err := p.command(); err != nil {
			return err
		}
	}
	if len(p.open) > 0 {
		return p.fail("a } closing every {")
	}
	for _, index := range p.branches {
		c := p.program.commands[index]
		if c.arg == "" {
			c.jump = len(p.program.commands)
			continue
		}
		target, ok := p.labels[c.arg]
		if !ok {
			return p.fail("a :" + c.arg + " label for " + string(c.name))
		}
		c.jump = target
	}
	return nil
}

func (p *parser) reserve(n int64) error {
	if n <= 0 {
		return nil
	}
	release, err := p.memory.Reserve(p.ctx, n)
	if err != nil {
		return err
	}
	p.program.releases = append(p.program.releases, release)
	return nil
}

func appendAdmitted[T any](p *parser, slice []T, elem T, elemBytes int64) ([]T, error) {
	if len(slice) < cap(slice) {
		return append(slice, elem), nil
	}
	nextCap := max(4, cap(slice)*2)
	if err := p.reserve(int64(nextCap) * elemBytes); err != nil {
		return nil, err
	}
	grown := make([]T, len(slice), nextCap)
	copy(grown, slice)
	return append(grown, elem), nil
}

func appendAdmittedBytes(p *parser, slice []byte, elems ...byte) ([]byte, error) {
	needed := len(slice) + len(elems)
	if needed <= cap(slice) {
		return append(slice, elems...), nil
	}
	nextCap := max(16, cap(slice)*2)
	for nextCap < needed {
		nextCap *= 2
	}
	if err := p.reserve(int64(nextCap)); err != nil {
		return nil, err
	}
	grown := make([]byte, len(slice), nextCap)
	copy(grown, slice)
	return append(grown, elems...), nil
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
	if err := p.reserve(commandStructBytes); err != nil {
		return err
	}
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
	var err error
	switch c.name {
	case '#':
		for p.pos < len(p.src) && p.src[p.pos] != '\n' {
			p.pos++
		}
		return nil
	case '{':
		if p.open, err = appendAdmitted(p, p.open, index, intElemBytes); err != nil {
			return err
		}
		p.program.commands, err = appendAdmitted(p, p.program.commands, c, pointerElemBytes)
		return err
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
		if err := p.reserve(int64(len(c.arg)) + labelMapEntryBytes); err != nil {
			return err
		}
		p.labels[c.arg] = index
		p.program.commands, err = appendAdmitted(p, p.program.commands, c, pointerElemBytes)
		return err
	case 'b', 't', 'T':
		c.arg = p.label()
		if err := p.reserve(int64(len(c.arg))); err != nil {
			return err
		}
		if p.branches, err = appendAdmitted(p, p.branches, index, intElemBytes); err != nil {
			return err
		}
		p.program.commands, err = appendAdmitted(p, p.program.commands, c, pointerElemBytes)
		return err
	case '=', 'd', 'D', 'g', 'G', 'h', 'H', 'n', 'N', 'p', 'P', 'x', 'z', 'F':
	case 'l', 'q', 'Q':
		c.number = p.number()
	case 'a', 'i', 'c':
		text, err := p.text()
		if err != nil {
			return err
		}
		c.text = text
		p.program.commands, err = appendAdmitted(p, p.program.commands, c, pointerElemBytes)
		return err
	case 'r', 'R', 'w', 'W':
		if c.arg = p.filename(); c.arg == "" {
			return p.fail("a file name after " + string(c.name))
		}
		if err := p.file(c.name, c.arg); err != nil {
			return err
		}
		p.program.commands, err = appendAdmitted(p, p.program.commands, c, pointerElemBytes)
		return err
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
	if p.program.commands, err = appendAdmitted(p, p.program.commands, c, pointerElemBytes); err != nil {
		return err
	}
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

func (p *parser) file(name byte, file string) error {
	list := &p.program.writes
	if name == 'r' || name == 'R' {
		list = &p.program.reads
	} else if file == Stdout || file == Stderr {
		return nil
	}
	for _, existing := range *list {
		if existing == file {
			return nil
		}
	}
	if err := p.reserve(int64(len(file))); err != nil {
		return err
	}
	var err error
	*list, err = appendAdmitted(p, *list, file, stringElemBytes)
	return err
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
func (p *parser) text() ([]byte, error) {
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
	var err error
	for p.pos < len(p.src) {
		c := p.src[p.pos]
		p.pos++
		switch {
		case c == '\n':
			return out, nil
		case c == '\\' && p.pos < len(p.src):
			if out, err = appendAdmittedBytes(p, out, p.src[p.pos]); err != nil {
				return nil, err
			}
			p.pos++
		default:
			if out, err = appendAdmittedBytes(p, out, c); err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}
