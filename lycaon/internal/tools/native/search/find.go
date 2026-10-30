package search

import (
	"errors"
	"io/fs"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/textsearch"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools/native/datetext"
	"mvdan.cc/sh/v3/pattern"
)

// Boolean nodes short-circuit before reaching predicates or actions.
type findNode struct {
	op          string // "and", "or", "not" or "primary"
	left, right *findNode
	test        func(*findRun, entry) (bool, error)
	structural  bool
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
	if !n.structural {
		var err error
		e, err = r.walker.inspect(e)
		if err != nil {
			if _, ok := ordinary(err); ok {
				return false, nil
			}
			return false, err
		}
	}
	return n.test(r, e)
}

// Negation binds before conjunction, then disjunction; parentheses group.
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
		return &findNode{op: "primary", test: f, structural: structuralPrimary(option.Key)}, nil
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
	case "-regex", "-iregex":
		m, err := textsearch.Compile(inv.ctx, textsearch.Spec{Patterns: []string{value}, Dialect: textsearch.Extended, Line: true, IgnoreCase: option.Key == "-iregex"}, inv.owners.regex)
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
		case "p", "s", "b", "c":
			letter := value[0]
			return test(func(_ *findRun, e entry) (bool, error) { return typeLetterOf(e.stat.Mode) == letter, nil })
		}
		return nil, inv.usage(value, "-type f, d, l, p, s, b or c")
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
	case "-mtime", "-atime", "-ctime":
		return r.ageTest(value, 24*time.Hour, option.Key+" [+-]N days", timeOf(option.Key))
	case "-mmin", "-amin", "-cmin":
		return r.ageTest(value, time.Minute, option.Key+" [+-]N minutes", timeOf(option.Key))
	case "-E", "-s", "-x", "-H", "-L", "-P", "-d":
		return nil, inv.usage(option.Spelling, option.Spelling+" before the starting points")
	case "-xdev":
		r.sameDevice = true
		return test(func(*findRun, entry) (bool, error) { return true, nil })
	case "-depth":
		if p := r.parser; p != nil {
			if word, ok := p.word(); ok {
				if _, digits := splitSign(word); digits != "" && strings.Trim(digits, "0123456789") == "" {
					p.pos++
					return r.depthTest(word)
				}
			}
		}
		r.depthFirst = true
		return test(func(*findRun, entry) (bool, error) { return true, nil })
	case "-uid", "-user":
		return r.ownerTest(value, option.Key, func(e entry) uint32 { return e.stat.UID })
	case "-gid", "-group":
		return r.ownerTest(value, option.Key, func(e entry) uint32 { return e.stat.GID })
	case "-true", "-false":
		truth := option.Key == "-true"
		return test(func(*findRun, entry) (bool, error) { return truth, nil })
	case "-perm":
		return r.permTest(value)
	case "-printf":
		r.actions = true
		return r.printfPrimary(value)
	case "-newer", "-neweraa", "-neweram", "-newerac", "-newerat", "-newerma", "-newermm", "-newermc", "-newermt", "-newerca", "-newercm", "-newercc", "-newerct":
		return r.newerTest(option.Key, value)
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
		case 'G':
			unit, digits = 1<<30, digits[:len(digits)-1]
		case 'T':
			unit, digits = 1<<40, digits[:len(digits)-1]
		case 'P':
			unit, digits = 1<<50, digits[:len(digits)-1]
		case 'w':
			unit, digits = 2, digits[:len(digits)-1]
		case 'b':
			digits = digits[:len(digits)-1]
		}
	}
	n, err := strconv.ParseInt(digits, 10, 64)
	if err != nil || n < 0 {
		return nil, r.inv.usage(value, "-size [+-]N[cwbkMGTP]")
	}
	return &findNode{op: "primary", test: func(_ *findRun, e entry) (bool, error) {
		units := (e.stat.Size + unit - 1) / unit
		return compare(units, n), nil
	}}, nil
}

// ageTest is -mtime and -mmin [+-]N: whole units since modification,
// truncated toward the past.
func (r *findRun) ageTest(value string, unit time.Duration, form string, at func(fseffect.EntryStat) time.Time) (*findNode, error) {
	compare, digits := splitSign(value)
	n, err := strconv.ParseInt(digits, 10, 64)
	if err != nil || n < 0 {
		return nil, r.inv.usage(value, form)
	}
	return &findNode{op: "primary", test: func(r *findRun, e entry) (bool, error) {
		age := r.now.Sub(at(e.stat))
		units := int64(age / unit)
		if age < 0 && age%unit != 0 {
			units--
		}
		return compare(units, n), nil
	}}, nil
}

// permTest is -perm MODE (exact permission and special bits), -MODE (all of
// them set) or /MODE (any of them set), with an octal MODE.
func (r *findRun) permTest(value string) (*findNode, error) {
	kind, digits := byte('='), value
	if strings.HasPrefix(value, "-") || strings.HasPrefix(value, "/") {
		kind, digits = value[0], value[1:]
	}
	want, err := strconv.ParseUint(digits, 8, 32)
	if err != nil || want > 0o7777 {
		return nil, r.inv.usage(value, "-perm [-/]OCTAL")
	}
	return &findNode{op: "primary", test: func(_ *findRun, e entry) (bool, error) {
		have := uint64(e.stat.Mode.Perm())
		if e.stat.Mode&fs.ModeSetuid != 0 {
			have |= 0o4000
		}
		if e.stat.Mode&fs.ModeSetgid != 0 {
			have |= 0o2000
		}
		if e.stat.Mode&fs.ModeSticky != 0 {
			have |= 0o1000
		}
		switch kind {
		case '-':
			return have&want == want, nil
		case '/':
			return want == 0 || have&want != 0, nil
		}
		return have == want, nil
	}}, nil
}

// ownerTest is -user/-uid and -group/-gid with a numeric id. Names are not
// resolved, and a platform without POSIX owners refuses rather than guessing.
func (r *findRun) ownerTest(value, key string, id func(entry) uint32) (*findNode, error) {
	want, err := strconv.ParseUint(value, 10, 32)
	if err != nil {
		return nil, r.inv.usage(value, key+" with a numeric id")
	}
	return &findNode{op: "primary", test: func(r *findRun, e entry) (bool, error) {
		if !e.stat.POSIX {
			return false, &toolrejection.ToolReject{Code: "COMMAND_PLATFORM_UNAVAILABLE", Data: map[string]any{"command": "find " + key, "capability": "POSIX owners"}}
		}
		return uint64(id(e)) == want, nil
	}}, nil
}

// depthTest compares depth below the starting point.
func (r *findRun) depthTest(value string) (*findNode, error) {
	compare, digits := splitSign(value)
	n, err := strconv.ParseInt(digits, 10, 64)
	if err != nil {
		return nil, r.inv.usage(value, "-depth [+-]N")
	}
	return &findNode{op: "primary", structural: true, test: func(_ *findRun, e entry) (bool, error) { return compare(int64(e.depth), n), nil }}, nil
}

// timeOf selects the timestamp an age or newer primary reads from its key's
// second byte: a for access, c for change, anything else modification.
func timeOf(key string) func(fseffect.EntryStat) time.Time {
	switch key[1] {
	case 'a':
		return func(st fseffect.EntryStat) time.Time { return st.AccessTime }
	case 'c':
		return func(st fseffect.EntryStat) time.Time { return st.ChangeTime }
	}
	return func(st fseffect.EntryStat) time.Time { return st.ModTime }
}

// newerTest is -newer REF and -newerXY REF: the entry's X time (access,
// modification or change) is after REF's Y time, or after the date REF when
// Y is t. -newer is -newermm. Dates read in the program's TZ.
func (r *findRun) newerTest(key, value string) (*findNode, error) {
	inv := r.inv
	primary := key
	if key == "-newer" {
		key = "-newermm"
	}
	entryTime := timeOf("-" + key[6:7])
	if key[7] == 't' {
		reference, err := datetext.Parse(value, r.now, datetext.Zone(inv.request.Environment))
		if err != nil {
			inv.fail(false, "I cannot figure out how to interpret '%s' as a date or time", value)
			return nil, errFindAbort
		}
		return &findNode{op: "primary", test: func(_ *findRun, e entry) (bool, error) {
			return entryTime(e.stat).After(reference), nil
		}}, nil
	}
	referenceTime := timeOf("-" + key[7:8])
	bound, err := inv.paths.Bind(inv.ctx, value, false)
	if err == nil && bound.Null {
		err = inv.usage(value, "a file for "+primary)
	}
	if err == nil {
		stat, statErr := bound.Stat(true)
		if statErr == nil {
			reference := referenceTime(stat)
			return &findNode{op: "primary", test: func(_ *findRun, e entry) (bool, error) {
				return entryTime(e.stat).After(reference), nil
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
		names, err := r.walker.directoryNames(e)
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
