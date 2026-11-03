// Package sedscript is the sed language. Parse reads a complete GNU sed
// script before any input is read; a Machine runs it over records with
// pattern and hold space, branches, append queues and per-file state. The
// host supplies the POSIX regex engine, the records, every output writer and
// the streams for r and R files, so the language never opens a path itself.
package sedscript

import (
	"context"
	"strconv"
)

// Regex is one compiled BRE or ERE with POSIX leftmost-longest matching.
type Regex interface {
	Find(ctx context.Context, subject []byte, from int) ([]int, error)
	Groups() int
}

// Compiler compiles BRE (extended false) or ERE text, case folded on request.
type Compiler interface {
	Compile(ctx context.Context, pattern string, extended, fold bool) (Regex, error)
}

// Stdout and Stderr are the w file names GNU sed writes to the streams.
const (
	Stdout = "/dev/stdout"
	Stderr = "/dev/stderr"
)

// Program is one parsed script and its admitted memory leases.
type Program struct {
	commands []*command
	quiet    bool
	writes   []string
	reads    []string
	releases []func()
}

// Close releases all program memory leases admitted while parsing the script.
func (p *Program) Close() {
	if p == nil {
		return
	}
	for i := len(p.releases) - 1; i >= 0; i-- {
		if p.releases[i] != nil {
			p.releases[i]()
		}
	}
	p.releases = nil
}

// Quiet reports a script whose first line is exactly #n, which means -n.
func (p *Program) Quiet() bool { return p.quiet }

// WriteFiles lists the distinct w, W and s///w files, in script order,
// without the standard streams. The host opens each before any input.
func (p *Program) WriteFiles() []string { return p.writes }

// ReadFiles lists the distinct r and R files in script order.
func (p *Program) ReadFiles() []string { return p.reads }

type addressKind uint8

const (
	addrLine addressKind = iota
	addrLast
	addrRegex
	addrStep     // first~step
	addrZero     // 0 in 0,/regex/
	addrRelative // second address +N
	addrMultiple // second address ~N
)

// address is one command address. A regex address with a nil regex is the
// empty regex, which reuses the last regex the run applied.
type address struct {
	kind  addressKind
	line  int
	step  int
	regex Regex
}

// command is one parsed command. arg is the label of : b t T or the file of
// r R w W; number is the exit code of q Q or the width of l (-1 when absent).
// jump is the branch target, or for { the index just past its }.
type command struct {
	first, second *address
	negate        bool
	name          byte
	text          []byte
	arg           string
	number        int
	sub           *substitution
	translate     map[rune]rune
	jump          int
	span          rangeState
}

// rangeState is a two-address command's per-file state; end is the last
// line of an addr,+N range.
type rangeState struct {
	active bool
	end    int
}

type substitution struct {
	regex    Regex
	pieces   []piece
	maxGroup int
	global   bool
	print    bool
	ordinal  int
	file     string
}

type pieceKind uint8

const (
	pieceLiteral pieceKind = iota
	pieceGroup
	pieceUpper    // \U
	pieceLower    // \L
	pieceEnd      // \E
	pieceOneUpper // \u
	pieceOneLower // \l
)

type piece struct {
	kind    pieceKind
	literal []byte
	group   int
}

// ScriptError is a script the grammar refuses. Unsupported marks a GNU form
// this language deliberately does not run (e, s///e, M).
type ScriptError struct {
	Offset      int
	Expected    string
	Unsupported bool
}

func (e *ScriptError) Error() string {
	return e.Expected + " at script byte " + strconv.Itoa(e.Offset)
}

// RegexError is the engine's refusal of one script regex.
type RegexError struct {
	Offset int
	Cause  error
}

func (e *RegexError) Error() string { return e.Cause.Error() }
func (e *RegexError) Unwrap() error { return e.Cause }

// LimitError reports a pattern or hold space over its byte ceiling, or a
// loop that ran its step ceiling without reading input.
type LimitError struct {
	Resource          string
	Maximum, Observed int64
}

func (e *LimitError) Error() string {
	return e.Resource + " exceeded: " + strconv.FormatInt(e.Observed, 10) + " > " + strconv.FormatInt(e.Maximum, 10)
}

// RuntimeError is a script fault only a run can reveal, such as an empty
// regex before any regex was applied.
type RuntimeError struct{ Reason string }

func (e *RuntimeError) Error() string { return e.Reason }
