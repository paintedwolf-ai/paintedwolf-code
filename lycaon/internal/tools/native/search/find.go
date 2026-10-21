package search

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/textsearch"
	"mvdan.cc/sh/v3/pattern"
)

// findNode is one parsed expression node. Boolean nodes short-circuit; a
// primary's test may act (print, exec, delete) and reports its truth.
type findNode struct {
	op          string // "and", "or", "not" or "primary"
	left, right *findNode
	test        func(*findRun, entry) (bool, error)
}

func (n *findNode) eval(r *findRun, e entry) (bool, error) {
	switch n.op {
	case "and":
		ok, err := n.left.eval(r, e)
		if err != nil || !ok {
			return false, err
		}
		return n.right.eval(r, e)
	case "or":
		ok, err := n.left.eval(r, e)
		if err != nil || ok {
			return ok, err
		}
		return n.right.eval(r, e)
	case "not":
		ok, err := n.left.eval(r, e)
		return !ok, err
	}
	return n.test(r, e)
}

// findParser builds the expression with GNU precedence: ! and -not bind
// tightest, then implicit or explicit -a, then -o; parentheses group.
type findParser struct {
	run   *findRun
	items []commandprogram.BoundItem
	pos   int
}

func (p *findParser) word() (string, bool) {
	if p.pos >= len(p.items) || p.items[p.pos].Operand == nil {
		return "", false
	}
	return p.items[p.pos].Operand.Value, true
}

func (p *findParser) option() *commandprogram.BoundOption {
	if p.pos >= len(p.items) {
		return nil
	}
	return p.items[p.pos].Option
}

func (p *findParser) parseOr() (*findNode, error) {
	left, err := p.parseAnd()
	if err != nil {
		return nil, err
	}
	for {
		option := p.option()
		if option == nil || option.Key != "-o" {
			return left, nil
		}
		p.pos++
		right, err := p.parseAnd()
		if err != nil {
			return nil, err
		}
		left = &findNode{op: "or", left: left, right: right}
	}
}

func (p *findParser) parseAnd() (*findNode, error) {
	left, err := p.parseUnary()
	if err != nil {
		return nil, err
	}
	for p.pos < len(p.items) {
		if option := p.option(); option != nil {
			if option.Key == "-o" {
				break
			}
			if option.Key == "-a" {
				p.pos++
			}
		} else if word, _ := p.word(); word == ")" {
			break
		}
		right, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		left = &findNode{op: "and", left: left, right: right}
	}
	return left, nil
}

func (p *findParser) parseUnary() (*findNode, error) {
	inv := p.run.inv
	if p.pos >= len(p.items) {
		return nil, inv.usage("", "an expression after an operator")
	}
	if option := p.option(); option != nil {
		if option.Key == "-not" {
			p.pos++
			x, err := p.parseUnary()
			if err != nil {
				return nil, err
			}
			return &findNode{op: "not", left: x}, nil
		}
		if option.Key == "-a" || option.Key == "-o" {
			return nil, inv.usage(option.Spelling, "an expression before "+option.Spelling)
		}
		p.pos++
		return p.run.primary(*option)
	}
	word, _ := p.word()
	switch word {
	case "!":
		p.pos++
		x, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		return &findNode{op: "not", left: x}, nil
	case "(":
		p.pos++
		if next, ok := p.word(); ok && next == ")" {
			return nil, inv.usage("()", "an expression inside parentheses")
		}
		x, err := p.parseOr()
		if err != nil {
			return nil, err
		}
		if next, ok := p.word(); !ok || next != ")" {
			return nil, inv.usage("(", "a closing )")
		}
		p.pos++
		return x, nil
	case ")":
		return nil, inv.usage(")", "an opening ( before )")
	}
	return nil, inv.usage(word, "paths before the expression; this word is neither a path nor a primary")
}

// errFindAbort stops find after an ordinary fatal diagnostic (status 1).
var errFindAbort = errors.New("find aborted")

func (r *findRun) primary(option commandprogram.BoundOption) (*findNode, error) {
	inv := r.inv
	value := ""
	if len(option.Values) > 0 {
		value = option.Values[0].Value
	}
	test := func(f func(*findRun, entry) (bool, error)) (*findNode, error) {
		return &findNode{op: "primary", test: f}, nil
	}
	switch option.Key {
	case "-name", "-iname", "-path", "-ipath":
		mode := pattern.EntireString
		if option.Key == "-iname" || option.Key == "-ipath" {
			mode |= pattern.NoGlobCase
		}
		expr, err := pattern.Regexp(value, mode)
		if err != nil {
			return nil, inv.usage(value, "a valid shell glob for "+option.Key)
		}
		re, err := regexp.Compile(expr)
		if err != nil {
			return nil, inv.usage(value, "a valid shell glob for "+option.Key)
		}
		byPath := option.Key == "-path" || option.Key == "-ipath"
		return test(func(_ *findRun, e entry) (bool, error) {
			if byPath {
				return re.MatchString(e.display), nil
			}
			return re.MatchString(e.name), nil
		})
	case "-regex":
		m, err := textsearch.Compile(inv.ctx, textsearch.Spec{Patterns: []string{value}, Dialect: textsearch.Extended, Line: true}, inv.owners.regex)
		if err != nil {
			return nil, err
		}
		return test(func(r *findRun, e entry) (bool, error) {
			_, ok, err := m.Select(r.inv.ctx, []byte(e.display))
			return ok, err
		})
	case "-type":
		switch value {
		case "f":
			return test(func(_ *findRun, e entry) (bool, error) { return e.file(), nil })
		case "d":
			return test(func(_ *findRun, e entry) (bool, error) { return e.dir(), nil })
		case "l":
			return test(func(_ *findRun, e entry) (bool, error) { return e.link, nil })
		}
		return nil, inv.usage(value, "-type f, d or l")
	case "-maxdepth", "-mindepth":
		n, err := strconv.Atoi(value)
		if err != nil || n < 0 {
			return nil, inv.usage(value, "a nonnegative depth for "+option.Key)
		}
		if option.Key == "-maxdepth" {
			r.maxDepth = n
		} else {
			r.minDepth = n
		}
		return test(func(*findRun, entry) (bool, error) { return true, nil })
	case "-size":
		return r.sizeTest(value)
	case "-empty":
		return test(func(r *findRun, e entry) (bool, error) { return r.empty(e) })
	case "-mtime":
		return r.mtimeTest(value)
	case "-newer":
		return r.newerTest(value)
	case "-prune":
		return test(func(r *findRun, e entry) (bool, error) {
			r.pruned = true
			return true, nil
		})
	case "-print", "-print0":
		r.actions = true
		terminator := "\n"
		if option.Key == "-print0" {
			terminator = "\x00"
		}
		return test(func(r *findRun, e entry) (bool, error) {
			return true, r.inv.out.text(e.display, terminator)
		})
	case "-exec":
		r.actions = true
		return r.execPrimary(option)
	case "-delete":
		r.actions = true
		r.depthFirst = true
		if inv.owners.Remover == nil {
			return nil, unavailable("find -delete", "the recoverable removal owner is not attached")
		}
		return test(func(r *findRun, e entry) (bool, error) { return r.remove(e) })
	}
	return nil, inv.usage(option.Spelling, "a catalogued find primary")
}

// sizeTest is -size [+-]N[ckMb]: the file's size rounded up to whole units.
func (r *findRun) sizeTest(value string) (*findNode, error) {
	compare, digits := splitSign(value)
	unit := int64(512)
	if digits != "" {
		switch digits[len(digits)-1] {
		case 'c':
			unit, digits = 1, digits[:len(digits)-1]
		case 'k':
			unit, digits = 1024, digits[:len(digits)-1]
		case 'M':
			unit, digits = 1024*1024, digits[:len(digits)-1]
		case 'b':
			digits = digits[:len(digits)-1]
		}
	}
	n, err := strconv.ParseInt(digits, 10, 64)
	if err != nil || n < 0 {
		return nil, r.inv.usage(value, "-size [+-]N[ckMb]")
	}
	return &findNode{op: "primary", test: func(_ *findRun, e entry) (bool, error) {
		units := (e.stat.Size + unit - 1) / unit
		return compare(units, n), nil
	}}, nil
}

// mtimeTest is -mtime [+-]N: whole days since modification, truncated.
func (r *findRun) mtimeTest(value string) (*findNode, error) {
	compare, digits := splitSign(value)
	n, err := strconv.ParseInt(digits, 10, 64)
	if err != nil || n < 0 {
		return nil, r.inv.usage(value, "-mtime [+-]N days")
	}
	return &findNode{op: "primary", test: func(r *findRun, e entry) (bool, error) {
		age := r.now.Sub(e.stat.ModTime)
		days := int64(age / (24 * time.Hour))
		if age < 0 && age%(24*time.Hour) != 0 {
			days--
		}
		return compare(days, n), nil
	}}, nil
}

func (r *findRun) newerTest(value string) (*findNode, error) {
	inv := r.inv
	bound, err := inv.paths.Bind(inv.ctx, value, false)
	if err == nil && bound.Null {
		err = inv.usage(value, "a file for -newer")
	}
	if err == nil {
		var st = bound
		stat, statErr := st.Stat(true)
		if statErr == nil {
			reference := stat.ModTime
			return &findNode{op: "primary", test: func(_ *findRun, e entry) (bool, error) {
				return e.stat.ModTime.After(reference), nil
			}}, nil
		}
		err = statErr
	}
	if reason, ok := ordinary(err); ok {
		inv.fail(false, "'%s': %s", value, reason)
		return nil, errFindAbort
	}
	return nil, err
}

// splitSign reads find's numeric comparison prefix.
func splitSign(value string) (func(a, b int64) bool, string) {
	switch {
	case strings.HasPrefix(value, "+"):
		return func(a, b int64) bool { return a > b }, value[1:]
	case strings.HasPrefix(value, "-"):
		return func(a, b int64) bool { return a < b }, value[1:]
	}
	return func(a, b int64) bool { return a == b }, value
}

func (r *findRun) empty(e entry) (bool, error) {
	switch {
	case e.link:
		return false, nil
	case e.file():
		return e.stat.Size == 0, nil
	case e.dir():
		if err := r.walker.visit(); err != nil {
			return false, err
		}
		names, err := e.bound.ReadDir()
		if err != nil {
			if reason, ok := ordinary(err); ok {
				r.inv.fail(false, "'%s': %s", e.display, reason)
				return false, nil
			}
			return false, err
		}
		return len(names) == 0, nil
	}
	return false, nil
}
