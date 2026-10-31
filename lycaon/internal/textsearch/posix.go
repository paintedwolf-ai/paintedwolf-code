package textsearch

import (
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/dlclark/regexp2/v2"
)

// posixPattern is one BRE or ERE translated to the standard Go grammar
// (PerlX only for the host's own non-capturing wrappers), with host markers
// for references and GNU word assertions the standard tree cannot spell.
type posixPattern struct {
	text   string
	groups int
	marked bool
}

// maxRepeat is the bounded interval ceiling; GNU accepts up to 32767.
const maxRepeat = 1000

const (
	asciiWordClass    = `[0-9A-Za-z_]`
	asciiNonWordClass = `[^0-9A-Za-z_]`
	asciiSpaceClass   = `[\t\n\v\f\r ]`
	asciiNonSpace     = `[^\t\n\v\f\r ]`
)

// translatePOSIX lexes one GNU BRE or ERE. Constructs outside the declared
// dialect are refused, never reinterpreted: an unknown alphanumeric escape is
// GREP_REGEX_UNSUPPORTED rather than GNU's warned literal.
// patternSpace admits the GNU sed escapes \n and \t for a subject that may
// hold newlines; grep records never do.
func translatePOSIX(pattern string, extended bool, groupOffset int, patternSpace bool) (posixPattern, error) {
	t := posixTranslator{src: pattern, extended: extended, offset: groupOffset, atomStart: -1, patternSpace: patternSpace}
	if err := t.run(); err != nil {
		return posixPattern{}, err
	}
	return posixPattern{text: t.out.String(), groups: t.groups, marked: t.marked}, nil
}

type posixTranslator struct {
	src          string
	extended     bool
	patternSpace bool
	offset       int
	pos          int
	out          strings.Builder
	groups       int
	open         []int // group numbers still open
	openAt       []int // output offsets of their opening parentheses
	closed       map[int]bool
	marked       bool
	// atomStart is the output offset of the last quantifiable atom, -1 when
	// a quantifier here would have nothing to repeat.
	atomStart int
	// quantified records the last quantifier appended, for GNU's
	// repeated-quantifier collapse ("a**" is "a*").
	quantified byte
}

func (t *posixTranslator) invalid(detail string) error {
	return &PatternError{Code: CodeRegexInvalid, Pattern: t.src, Offset: t.pos, Detail: detail}
}

func (t *posixTranslator) unsupported(detail string) error {
	return &PatternError{Code: CodeRegexUnsupported, Pattern: t.src, Offset: t.pos, Detail: detail}
}

// expressionStart reports a position where GNU treats * (and ERE + ? {) as
// literal and BRE ^ as an anchor: the start, after a group opening or after
// an alternation.
func (t *posixTranslator) expressionStart() bool {
	return t.atomStart == -1 && t.quantified == 0
}

func (t *posixTranslator) run() error {
	t.closed = map[int]bool{}
	for t.pos < len(t.src) {
		c := t.src[t.pos]
		switch {
		case c == '\\':
			if err := t.escape(); err != nil {
				return err
			}
		case c == '[':
			if err := t.bracket(); err != nil {
				return err
			}
		case c == '.':
			t.atom(".")
			t.pos++
		case c == '*':
			if t.expressionStart() {
				t.atom(`\*`)
				t.pos++
				continue
			}
			t.pos++
			if err := t.quantify('*'); err != nil {
				return err
			}
		case t.extended && (c == '+' || c == '?'):
			if t.expressionStart() {
				t.atom(`\` + string(c))
				t.pos++
				continue
			}
			t.pos++
			if err := t.quantify(c); err != nil {
				return err
			}
		case t.extended && c == '{':
			if t.expressionStart() || !t.intervalAhead(t.pos+1) {
				t.atom(`\{`)
				t.pos++
				continue
			}
			t.pos++
			if err := t.interval(); err != nil {
				return err
			}
		case t.extended && c == '(':
			t.pos++
			t.openGroup()
		case t.extended && c == ')':
			t.pos++
			if err := t.closeGroup(); err != nil {
				return err
			}
		case t.extended && c == '|':
			t.pos++
			t.alternate()
		case c == '^':
			t.pos++
			if t.extended || t.expressionStart() {
				t.anchor("^")
			} else {
				t.atom(`\^`)
			}
		case c == '$':
			t.pos++
			if t.extended || t.atEndOfBREBranch() {
				t.anchor("$")
			} else {
				t.atom(`\$`)
			}
		default:
			r, size := utf8.DecodeRuneInString(t.src[t.pos:])
			if regexp2.IsMarker(r) {
				return t.unsupported("private-use scalar reserved for host assertions")
			}
			t.pos += size
			t.literal(r)
		}
	}
	if len(t.open) > 0 {
		return t.invalid("unmatched ( or \\(")
	}
	return nil
}

// atEndOfBREBranch reports a BRE $ that GNU treats as an anchor: at the end
// of the pattern, before \) or before \|.
func (t *posixTranslator) atEndOfBREBranch() bool {
	rest := t.src[t.pos:]
	return rest == "" || strings.HasPrefix(rest, `\)`) || strings.HasPrefix(rest, `\|`)
}

func (t *posixTranslator) literal(r rune) {
	if r < utf8.RuneSelf && strings.ContainsRune(`\.+*?()|[]{}^$`, r) {
		t.atom(`\` + string(r))
		return
	}
	t.atom(string(r))
}

func (t *posixTranslator) atom(text string) {
	t.atomStart = t.out.Len()
	t.quantified = 0
	t.out.WriteString(text)
}

func (t *posixTranslator) anchor(text string) {
	t.out.WriteString(text)
	t.atomStart, t.quantified = -1, 0
}

func (t *posixTranslator) marker(r rune, quantifiable bool) {
	t.marked = true
	if quantifiable {
		t.atom(string(r))
		return
	}
	t.anchor(string(r))
}

func (t *posixTranslator) openGroup() {
	t.groups++
	t.open = append(t.open, t.groups)
	t.openAt = append(t.openAt, t.out.Len())
	t.out.WriteString("(")
	t.atomStart, t.quantified = -1, 0
}

func (t *posixTranslator) closeGroup() error {
	if len(t.open) == 0 {
		return t.invalid("unmatched ) or \\)")
	}
	last := len(t.open) - 1
	group, start := t.open[last], t.openAt[last]
	t.open, t.openAt = t.open[:last], t.openAt[:last]
	t.closed[group] = true
	t.out.WriteString(")")
	// The whole group is the atom a following quantifier repeats.
	t.atomStart, t.quantified = start, 0
	return nil
}

func (t *posixTranslator) alternate() {
	t.out.WriteString("|")
	t.atomStart, t.quantified = -1, 0
}

// quantify appends or collapses one closure operator. GNU accepts a run of
// closures; the standard grammar refuses it, so the run is reduced to the
// equivalent single operator.
func (t *posixTranslator) quantify(op byte) error {
	if t.quantified != 0 {
		if t.quantified == '{' {
			return t.unsupported("closure after an interval")
		}
		combined := combineClosures(t.quantified, op)
		s := t.out.String()
		t.out.Reset()
		t.out.WriteString(s[:len(s)-1])
		t.out.WriteByte(combined)
		t.quantified = combined
		return nil
	}
	if t.atomStart < 0 {
		return t.invalid("nothing to repeat")
	}
	t.out.WriteByte(op)
	t.quantified = op
	return nil
}

func combineClosures(first, second byte) byte {
	if first == second && first != '*' {
		return first
	}
	return '*'
}

// intervalAhead reports whether src[at:] begins a GNU ERE interval body
// ("m}", "m,}", "m,n}", ",n}"); otherwise an ERE brace is literal.
func (t *posixTranslator) intervalAhead(at int) bool {
	end := strings.IndexByte(t.src[at:], '}')
	if end < 0 {
		return false
	}
	body := t.src[at : at+end]
	low, high, comma := strings.Cut(body, ",")
	if !digits(low) && !(comma && low == "") {
		return false
	}
	return !comma || high == "" || digits(high)
}

func digits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// interval consumes an interval body after its opening brace and its closing
// brace (BRE: "\}").
func (t *posixTranslator) interval() error {
	closer := "}"
	if !t.extended {
		closer = `\}`
	}
	end := strings.Index(t.src[t.pos:], closer)
	if end < 0 {
		return t.invalid("unmatched \\{")
	}
	body := t.src[t.pos : t.pos+end]
	t.pos += end + len(closer)
	low, high, comma := strings.Cut(body, ",")
	if low == "" && comma {
		low = "0"
	}
	if !digits(low) || (comma && high != "" && !digits(high)) {
		return t.invalid("invalid content of \\{\\}")
	}
	minimum, _ := strconv.Atoi(low)
	maximum := minimum
	if comma {
		maximum = -1
		if high != "" {
			maximum, _ = strconv.Atoi(high)
		}
	}
	if maximum >= 0 && minimum > maximum {
		return t.invalid("invalid content of \\{\\}")
	}
	if minimum > 32767 || maximum > 32767 {
		return t.invalid("regular expression too big")
	}
	if minimum > maxRepeat || maximum > maxRepeat {
		return t.unsupported("interval count above " + strconv.Itoa(maxRepeat))
	}
	if t.quantified != 0 {
		return t.unsupported("interval after a closure")
	}
	if t.atomStart < 0 {
		return t.invalid("invalid preceding regular expression")
	}
	t.out.WriteString("{" + strconv.Itoa(minimum))
	switch {
	case !comma:
	case maximum < 0:
		t.out.WriteString(",")
	default:
		t.out.WriteString("," + strconv.Itoa(maximum))
	}
	t.out.WriteString("}")
	t.quantified = '{'
	return nil
}

func (t *posixTranslator) escape() error {
	if t.pos+1 >= len(t.src) {
		t.pos++
		return t.invalid("trailing backslash")
	}
	r, size := utf8.DecodeRuneInString(t.src[t.pos+1:])
	t.pos += 1 + size
	if !t.extended {
		switch r {
		case '(':
			t.openGroup()
			return nil
		case ')':
			return t.closeGroup()
		case '|':
			t.alternate()
			return nil
		case '{':
			if t.expressionStart() {
				return t.invalid("invalid preceding regular expression")
			}
			return t.interval()
		case '}':
			return t.invalid("unmatched \\}")
		case '+', '?':
			if t.expressionStart() {
				t.atom(`\` + string(r))
				return nil
			}
			if r == '+' {
				return t.quantify('+')
			}
			return t.quantify('?')
		}
	}
	switch {
	case r >= '1' && r <= '9':
		group := int(r - '0')
		if group > t.groups || !t.closed[group] {
			return t.invalid("invalid back reference")
		}
		markedGroup := t.offset + group
		if markedGroup < 0 || markedGroup > regexp2.MaxMarkedGroup {
			return t.unsupported("back reference beyond the marked group ceiling")
		}
		t.marker(regexp2.MarkerBackref+rune(markedGroup), true)
	case r == '<':
		t.marker(regexp2.MarkerWordStart, false)
	case r == '>':
		t.marker(regexp2.MarkerWordEnd, false)
	case r == 'b':
		t.marker(regexp2.MarkerWordBoundary, false)
	case r == 'B':
		t.marker(regexp2.MarkerNotWordBoundary, false)
	case t.patternSpace && r == 'n':
		t.atom(`\n`)
	case t.patternSpace && r == 't':
		t.atom(`\t`)
	case r == 'w':
		t.atom(asciiWordClass)
	case r == 'W':
		t.atom(asciiNonWordClass)
	case r == 's':
		t.atom(asciiSpaceClass)
	case r == 'S':
		t.atom(asciiNonSpace)
	case r < utf8.RuneSelf && (r >= '0' && r <= '9' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z'):
		return t.unsupported(`escape \` + string(r) + " is outside the declared POSIX grammar")
	default:
		if regexp2.IsMarker(r) {
			return t.unsupported("private-use scalar reserved for host assertions")
		}
		t.literal(r)
	}
	return nil
}

// bracket copies one POSIX bracket expression. Inside it a backslash is an
// ordinary scalar, and a leading ] is a member.
func (t *posixTranslator) bracket() error {
	start := t.pos
	t.pos++
	var out strings.Builder
	out.WriteByte('[')
	if t.pos < len(t.src) && t.src[t.pos] == '^' {
		out.WriteByte('^')
		t.pos++
	}
	first := true
	for {
		if t.pos >= len(t.src) {
			t.pos = start
			return t.invalid("unmatched [, [^, [:, [., or [=")
		}
		c := t.src[t.pos]
		if c == ']' && !first {
			t.pos++
			break
		}
		first = false
		if c == '[' && t.pos+1 < len(t.src) && strings.ContainsRune(":=.", rune(t.src[t.pos+1])) {
			kind := t.src[t.pos+1]
			end := strings.Index(t.src[t.pos+2:], string(kind)+"]")
			if end < 0 {
				t.pos = start
				return t.invalid("unmatched [, [^, [:, [., or [=")
			}
			name := t.src[t.pos+2 : t.pos+2+end]
			if kind == ':' && !posixClasses[name] {
				return t.invalid("invalid character class")
			}
			out.WriteString("[" + string(kind) + name + string(kind) + "]")
			t.pos += 2 + end + 2
			continue
		}
		r, size := utf8.DecodeRuneInString(t.src[t.pos:])
		if regexp2.IsMarker(r) {
			return t.unsupported("private-use scalar reserved for host assertions")
		}
		t.pos += size
		switch r {
		case '\\', '[', ']', '^':
			out.WriteString(`\` + string(r))
		case '-':
			// A range operator stays bare between two members; an edge
			// hyphen is a member.
			if out.Len() == 1 || strings.HasSuffix(out.String(), "[^") || t.pos >= len(t.src) || t.src[t.pos] == ']' {
				out.WriteString(`\-`)
			} else {
				out.WriteByte('-')
			}
		default:
			out.WriteRune(r)
		}
	}
	out.WriteByte(']')
	t.atom(out.String())
	return nil
}

var posixClasses = map[string]bool{
	"alnum": true, "alpha": true, "blank": true, "cntrl": true, "digit": true, "graph": true,
	"lower": true, "print": true, "punct": true, "space": true, "upper": true, "xdigit": true,
}
