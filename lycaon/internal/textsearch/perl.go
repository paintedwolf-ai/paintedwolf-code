package textsearch

import (
	"strconv"
	"strings"
	"unicode/utf8"
)

// GrammarPCRECommonV1 names the bounded PCRE-compatible grammar of P01's
// regex contract. It is a declared subset of PCRE2 pattern syntax, executed
// by the bounded backtracking engine; it is never RE2.
const GrammarPCRECommonV1 = "pcre-common-v1"

// perlPattern is one validated pcre-common-v1 pattern rewritten into the
// engine grammar: every class escape and word boundary is spelled with its
// fixed ASCII set, so no engine Unicode default applies.
type perlPattern struct {
	text   string
	groups int
}

const (
	pcreWord    = `0-9A-Za-z_`
	pcreDigit   = `0-9`
	pcreSpace   = `\t\n\x0B\f\r `
	pcreWordSet = `[` + pcreWord + `]`
)

var pcreClassEscapes = map[byte]struct {
	members string
	negated bool
}{
	'd': {pcreDigit, false}, 'D': {pcreDigit, true},
	'w': {pcreWord, false}, 'W': {pcreWord, true},
	's': {pcreSpace, false}, 'S': {pcreSpace, true},
}

var pcreControlEscapes = map[byte]string{'n': `\n`, 't': `\t`, 'r': `\r`, 'f': `\f`, 'e': `\x1B`, 'a': `\x07`}

// translatePerl validates one pattern against pcre-common-v1. Recursion,
// subroutines, conditionals, callouts, verbs, named or atomic groups, inline
// options, possessive quantifiers, Unicode properties, variable-width
// lookbehind and unknown escapes are refused with GREP_REGEX_UNSUPPORTED.
func translatePerl(pattern string, groupOffset int) (perlPattern, error) {
	t := perlTranslator{src: pattern, offset: groupOffset, closed: map[int]bool{}}
	if _, err := t.alternatives(0); err != nil {
		return perlPattern{}, err
	}
	if t.pos < len(t.src) {
		return perlPattern{}, t.invalid("unmatched closing parenthesis")
	}
	return perlPattern{text: t.out.String(), groups: t.groups}, nil
}

type perlTranslator struct {
	src    string
	pos    int
	offset int
	out    strings.Builder
	groups int
	closed map[int]bool
	// behind counts enclosing lookbehinds; their bodies must be fixed width.
	behind int
}

func (t *perlTranslator) invalid(detail string) error {
	return &PatternError{Code: CodeRegexInvalid, Pattern: t.src, Offset: t.pos, Detail: detail}
}

func (t *perlTranslator) unsupported(detail string) error {
	return &PatternError{Code: CodeRegexUnsupported, Pattern: t.src, Offset: t.pos, Detail: detail}
}

// width tracks one alternative's scalar width for lookbehind admission.
type width struct {
	n     int
	fixed bool
}

// alternatives translates alternatives until an unmatched ")" or the end and
// returns each alternative's width.
func (t *perlTranslator) alternatives(depth int) ([]width, error) {
	var widths []width
	current := width{fixed: true}
	for t.pos < len(t.src) {
		c := t.src[t.pos]
		switch c {
		case '|':
			t.pos++
			t.out.WriteByte('|')
			widths = append(widths, current)
			current = width{fixed: true}
			continue
		case ')':
			if depth == 0 {
				return nil, t.invalid("unmatched closing parenthesis")
			}
			return append(widths, current), nil
		}
		w, quantifiable, err := t.atom(depth)
		if err != nil {
			return nil, err
		}
		w, err = t.quantifier(w, quantifiable)
		if err != nil {
			return nil, err
		}
		current.n += w.n
		current.fixed = current.fixed && w.fixed
	}
	if depth > 0 {
		return nil, t.invalid("missing closing parenthesis")
	}
	return append(widths, current), nil
}

func (t *perlTranslator) atom(depth int) (width, bool, error) {
	c := t.src[t.pos]
	switch c {
	case '\\':
		return t.escape()
	case '[':
		return t.class()
	case '(':
		return t.group(depth)
	case '.':
		t.pos++
		t.out.WriteByte('.')
		return width{1, true}, true, nil
	case '^', '$':
		t.pos++
		t.out.WriteByte(c)
		return width{0, true}, false, nil
	case '*', '+', '?':
		return width{}, false, t.invalid("quantifier does not follow a repeatable item")
	case '{':
		if _, _, _, ok := braceQuantifier(t.src[t.pos:]); ok {
			return width{}, false, t.invalid("quantifier does not follow a repeatable item")
		}
		t.pos++
		t.out.WriteString(`\{`)
		return width{1, true}, true, nil
	}
	r, size := utf8.DecodeRuneInString(t.src[t.pos:])
	if r == utf8.RuneError && size == 1 {
		return width{}, false, t.invalid("pattern is not UTF-8")
	}
	t.pos += size
	t.literal(r)
	return width{1, true}, true, nil
}

func (t *perlTranslator) literal(r rune) {
	if r < utf8.RuneSelf && strings.ContainsRune(`\.+*?()|[]{}^$#- `, r) {
		t.out.WriteString(`\`)
	}
	t.out.WriteRune(r)
}

// braceQuantifier parses "{m}", "{m,}" or "{m,n}" at s; anything else is a
// literal brace in PCRE, except "{,n}", whose meaning varies by PCRE release
// and is refused.
func braceQuantifier(s string) (minimum, maximum, size int, ok bool) {
	end := strings.IndexByte(s, '}')
	if !strings.HasPrefix(s, "{") || end < 0 {
		return 0, 0, 0, false
	}
	body := s[1:end]
	low, high, comma := strings.Cut(body, ",")
	if !digits(low) || (comma && high != "" && !digits(high)) {
		return 0, 0, 0, false
	}
	minimum, _ = strconv.Atoi(low)
	maximum = minimum
	if comma {
		maximum = -1
		if high != "" {
			maximum, _ = strconv.Atoi(high)
		}
	}
	return minimum, maximum, end + 1, true
}

func (t *perlTranslator) quantifier(w width, quantifiable bool) (width, error) {
	if t.pos >= len(t.src) {
		return w, nil
	}
	c := t.src[t.pos]
	var minimum, maximum int
	switch c {
	case '*':
		minimum, maximum = 0, -1
		t.pos++
	case '+':
		minimum, maximum = 1, -1
		t.pos++
	case '?':
		minimum, maximum = 0, 1
		t.pos++
	case '{':
		if strings.HasPrefix(t.src[t.pos:], "{,") {
			return w, t.unsupported("{,n} quantifier")
		}
		lo, hi, size, ok := braceQuantifier(t.src[t.pos:])
		if !ok {
			return w, nil
		}
		if hi >= 0 && lo > hi {
			return w, t.invalid("numbers out of order in {} quantifier")
		}
		if lo > maxRepeat || hi > maxRepeat {
			return w, t.unsupported("quantifier count above " + strconv.Itoa(maxRepeat))
		}
		minimum, maximum = lo, hi
		t.pos += size
	default:
		return w, nil
	}
	if !quantifiable {
		return w, t.unsupported("quantified assertion")
	}
	t.out.WriteString(t.src[t.pos-quantifierLength(t.src, t.pos, c) : t.pos])
	if t.pos < len(t.src) {
		switch t.src[t.pos] {
		case '?':
			t.pos++
			t.out.WriteByte('?')
		case '+':
			return w, t.unsupported("possessive quantifier")
		}
	}
	if t.pos < len(t.src) && strings.ContainsRune("*+?", rune(t.src[t.pos])) {
		return w, t.invalid("nested quantifier")
	}
	if t.pos < len(t.src) && t.src[t.pos] == '{' {
		if _, _, _, ok := braceQuantifier(t.src[t.pos:]); ok {
			return w, t.invalid("nested quantifier")
		}
	}
	if minimum == maximum {
		return width{w.n * minimum, w.fixed}, nil
	}
	return width{fixed: false}, nil
}

// quantifierLength is the byte length of the quantifier ending at end.
func quantifierLength(src string, end int, first byte) int {
	if first != '{' {
		return 1
	}
	return end - strings.LastIndexByte(src[:end], '{')
}

func (t *perlTranslator) escape() (width, bool, error) {
	if t.pos+1 >= len(t.src) {
		t.pos++
		return width{}, false, t.invalid("trailing backslash")
	}
	c := t.src[t.pos+1]
	t.pos += 2
	if set, ok := pcreClassEscapes[c]; ok {
		if set.negated {
			t.out.WriteString("[^" + set.members + "]")
		} else {
			t.out.WriteString("[" + set.members + "]")
		}
		return width{1, true}, true, nil
	}
	if control, ok := pcreControlEscapes[c]; ok {
		t.out.WriteString(control)
		return width{1, true}, true, nil
	}
	switch {
	case c == 'b':
		t.out.WriteString(`(?:(?<=` + pcreWordSet + `)(?!` + pcreWordSet + `)|(?<!` + pcreWordSet + `)(?=` + pcreWordSet + `))`)
		return width{0, true}, false, nil
	case c == 'B':
		t.out.WriteString(`(?:(?<=` + pcreWordSet + `)(?=` + pcreWordSet + `)|(?<!` + pcreWordSet + `)(?!` + pcreWordSet + `))`)
		return width{0, true}, false, nil
	case c >= '1' && c <= '9':
		group := int(c - '0')
		if t.pos < len(t.src) && t.src[t.pos] >= '0' && t.src[t.pos] <= '9' {
			return width{}, false, t.unsupported("multi-digit back reference")
		}
		if group > countCaptureGroups(t.src) {
			return width{}, false, t.invalid("reference to non-existent subpattern")
		}
		if !t.closed[group] {
			return width{}, false, t.unsupported("back reference to a group that has not completed")
		}
		if t.behind > 0 {
			return width{}, false, t.unsupported("back reference inside lookbehind")
		}
		t.out.WriteString(`\` + strconv.Itoa(t.offset+group))
		return width{fixed: false}, true, nil
	case c == 'x':
		return t.hexEscape()
	case c < utf8.RuneSelf && (c >= '0' && c <= '9' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z'):
		return width{}, false, t.unsupported(`escape \` + string(c) + " is outside " + GrammarPCRECommonV1)
	case c >= utf8.RuneSelf:
		t.pos -= 1
		r, size := utf8.DecodeRuneInString(t.src[t.pos:])
		t.pos += size
		t.literal(r)
		return width{1, true}, true, nil
	}
	t.literal(rune(c))
	return width{1, true}, true, nil
}

// hexEscape accepts \xHH and \x{H...}, writing the scalar as a literal.
func (t *perlTranslator) hexEscape() (width, bool, error) {
	var digitsText string
	if strings.HasPrefix(t.src[t.pos:], "{") {
		end := strings.IndexByte(t.src[t.pos:], '}')
		if end < 0 {
			return width{}, false, t.invalid("missing } in \\x{}")
		}
		digitsText = t.src[t.pos+1 : t.pos+end]
		t.pos += end + 1
	} else {
		n := 0
		for n < 2 && t.pos+n < len(t.src) && strings.ContainsRune("0123456789abcdefABCDEF", rune(t.src[t.pos+n])) {
			n++
		}
		digitsText = t.src[t.pos : t.pos+n]
		t.pos += n
	}
	value, err := strconv.ParseUint(digitsText, 16, 32)
	if digitsText == "" || err != nil || value > utf8.MaxRune || (value >= 0xD800 && value <= 0xDFFF) {
		return width{}, false, t.invalid("invalid \\x escape")
	}
	t.literal(rune(value))
	return width{1, true}, true, nil
}

func (t *perlTranslator) group(depth int) (width, bool, error) {
	open := t.pos
	t.pos++
	kind := ""
	if strings.HasPrefix(t.src[t.pos:], "?") {
		for _, prefix := range []string{"?:", "?=", "?!", "?<=", "?<!"} {
			if strings.HasPrefix(t.src[t.pos:], prefix) {
				kind = prefix
				break
			}
		}
		if kind == "" {
			return width{}, false, t.unsupported("group syntax (" + t.src[t.pos:min(len(t.src), t.pos+3)] + " is outside " + GrammarPCRECommonV1)
		}
		t.pos += len(kind)
	} else if strings.HasPrefix(t.src[t.pos:], "*") {
		return width{}, false, t.unsupported("backtracking control verb")
	}
	number := 0
	if kind == "" {
		t.groups++
		number = t.groups
		if t.offset+number > 999 {
			return width{}, false, t.unsupported("too many capture groups")
		}
	}
	t.out.WriteString("(" + kind)
	behind := kind == "?<=" || kind == "?<!"
	if behind {
		t.behind++
	}
	widths, err := t.alternatives(depth + 1)
	if behind {
		t.behind--
	}
	if err != nil {
		return width{}, false, err
	}
	if t.pos >= len(t.src) || t.src[t.pos] != ')' {
		t.pos = open
		return width{}, false, t.invalid("missing closing parenthesis")
	}
	t.pos++
	t.out.WriteByte(')')
	if number > 0 {
		t.closed[number] = true
	}
	if behind {
		// PCRE admits differing fixed widths only across top-level
		// alternatives of the lookbehind itself.
		for _, w := range widths {
			if !w.fixed {
				return width{}, false, t.unsupported("variable-width lookbehind")
			}
		}
	}
	switch kind {
	case "?=", "?!", "?<=", "?<!":
		return width{0, true}, false, nil
	}
	common := width{fixed: true}
	for i, w := range widths {
		if !w.fixed || (i > 0 && w.n != common.n) {
			return width{fixed: false}, true, nil
		}
		common.n = w.n
	}
	return common, true, nil
}

// class copies one character class, expanding class escapes and POSIX
// classes to their ASCII members and escaping every engine-special scalar.
func (t *perlTranslator) class() (width, bool, error) {
	start := t.pos
	t.pos++
	var out strings.Builder
	out.WriteByte('[')
	if t.pos < len(t.src) && t.src[t.pos] == '^' {
		out.WriteByte('^')
		t.pos++
	}
	first := true
	// single is true after one literal scalar that a following hyphen may
	// use as a range start; class expansions and range ends cannot.
	single, rangeEnd := false, false
	for {
		if t.pos >= len(t.src) {
			t.pos = start
			return width{}, false, t.invalid("missing terminating ] for character class")
		}
		c := t.src[t.pos]
		if c == ']' && !first {
			t.pos++
			break
		}
		first = false
		switch {
		case c == '[' && strings.HasPrefix(t.src[t.pos:], "[:"):
			end := strings.Index(t.src[t.pos+2:], ":]")
			if end < 0 {
				t.pos++
				out.WriteString(`\[`)
				continue
			}
			name := t.src[t.pos+2 : t.pos+2+end]
			members, ok := pcrePOSIXClasses[name]
			if !ok {
				return width{}, false, t.invalid("unknown POSIX class name")
			}
			if rangeEnd {
				return width{}, false, t.invalid("invalid range in character class")
			}
			out.WriteString(members)
			t.pos += 2 + end + 2
			single = false
		case c == '\\':
			if t.pos+1 >= len(t.src) {
				return width{}, false, t.invalid("trailing backslash")
			}
			e := t.src[t.pos+1]
			t.pos += 2
			if set, ok := pcreClassEscapes[e]; ok {
				if set.negated {
					return width{}, false, t.unsupported(`negated class escape \` + string(e) + " inside a class")
				}
				if rangeEnd {
					return width{}, false, t.invalid("invalid range in character class")
				}
				out.WriteString(set.members)
				single = false
				continue
			}
			if control, ok := pcreControlEscapes[e]; ok {
				out.WriteString(control)
				single, rangeEnd = !rangeEnd, false
				continue
			}
			if e == 'x' {
				mark := t.out.Len()
				if _, _, err := t.hexEscape(); err != nil {
					return width{}, false, err
				}
				literal := t.out.String()[mark:]
				s := t.out.String()[:mark]
				t.out.Reset()
				t.out.WriteString(s)
				out.WriteString(literal)
				single, rangeEnd = !rangeEnd, false
				continue
			}
			if e < utf8.RuneSelf && (e >= '0' && e <= '9' || e >= 'A' && e <= 'Z' || e >= 'a' && e <= 'z') {
				return width{}, false, t.unsupported(`escape \` + string(e) + " inside a class is outside " + GrammarPCRECommonV1)
			}
			t.pos--
			r, size := utf8.DecodeRuneInString(t.src[t.pos:])
			t.pos += size
			classLiteral(&out, r)
			single, rangeEnd = !rangeEnd, false
		default:
			r, size := utf8.DecodeRuneInString(t.src[t.pos:])
			if r == utf8.RuneError && size == 1 {
				return width{}, false, t.invalid("pattern is not UTF-8")
			}
			t.pos += size
			if r == '-' && single && t.pos < len(t.src) && t.src[t.pos] != ']' {
				out.WriteByte('-')
				single, rangeEnd = false, true
				continue
			}
			classLiteral(&out, r)
			single = !rangeEnd
			rangeEnd = false
		}
	}
	t.out.WriteString(out.String() + "]")
	return width{1, true}, true, nil
}

func classLiteral(out *strings.Builder, r rune) {
	if strings.ContainsRune(`\]^-[`, r) {
		out.WriteByte('\\')
	}
	out.WriteRune(r)
}

var pcrePOSIXClasses = map[string]string{
	"alnum": `0-9A-Za-z`, "alpha": `A-Za-z`, "ascii": `\x00-\x7F`, "blank": `\t `, "cntrl": `\x00-\x1F\x7F`,
	"digit": `0-9`, "graph": `!-~`, "lower": `a-z`, "print": ` -~`, "punct": `!-/:-@\[-` + "`" + `{-~`,
	"space": `\t\n\x0B\f\r `, "upper": `A-Z`, "word": `0-9A-Za-z_`, "xdigit": `0-9A-Fa-f`,
}

// countCaptureGroups counts unnamed capturing parentheses outside classes.
func countCaptureGroups(src string) int {
	n := 0
	inClass := false
	for i := 0; i < len(src); i++ {
		switch c := src[i]; {
		case c == '\\':
			i++
		case inClass:
			if c == ']' {
				inClass = false
			}
		case c == '[':
			inClass = true
			if i+1 < len(src) && src[i+1] == '^' {
				i++
			}
			if i+1 < len(src) && src[i+1] == ']' {
				i++
			}
		case c == '(' && (i+1 >= len(src) || src[i+1] != '?'):
			n++
		}
	}
	return n
}
