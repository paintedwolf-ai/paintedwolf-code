package argv

import (
	"fmt"
	"strconv"
	"strings"
)

// escapableChars lose their meaning after an unquoted backslash; a backslash
// before any other character is kept as written.
const escapableChars = " \t;|&$`<>'\"\\*?[]#(){}~"

// GlobChars are the pattern characters an unquoted word may carry.
const GlobChars = "*?["

// EscapeGlob restates literal text as a glob pattern that matches only itself,
// escaping as a quoted character is in Word.Pattern.
func EscapeGlob(text string) string {
	var b strings.Builder
	for _, r := range text {
		if strings.ContainsRune(GlobChars+`\]`, r) {
			b.WriteRune('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// Word is one argument after quote removal.
type Word struct {
	Text string
	// Pattern restates Text as a glob pattern: unquoted `*`, `?`, and `[` stay
	// active and every quoted or escaped character is backslash-escaped. It is
	// empty when the word carries no unquoted glob character.
	Pattern string
	// Addressed reports that Text begins with an unquoted `@`, the sigil of a
	// host path address such as `@scratch/notes.md`. Quoting it keeps the
	// word literal.
	Addressed bool
	// eq is the byte offset of the first unquoted `=` in Text, or -1.
	eq int
}

// lexedElement is one argv vector before environment assignments are split off.
type lexedElement struct {
	connector Connector
	words     []Word
	redirects []Redirect
}

type lexer struct {
	src []rune
	pos int
}

func (l *lexer) eof() bool { return l.pos >= len(l.src) }

func (l *lexer) peek() rune { return l.src[l.pos] }

func (l *lexer) peekAt(offset int) (rune, bool) {
	if l.pos+offset >= len(l.src) {
		return 0, false
	}
	return l.src[l.pos+offset], true
}

func (l *lexer) skipSeparators() {
	for !l.eof() && argSeparator(l.peek()) {
		l.pos++
	}
}

// operatorRune ends an unquoted word.
func operatorRune(r rune) bool {
	return r == ';' || r == '&' || r == '|' || r == '<' || r == '>'
}

// lexLine splits one command line into argv vectors joined by connectors.
func lexLine(line string) ([]lexedElement, error) {
	line = trimCommandLine(line)
	if line == "" {
		return nil, ErrCommandRequired
	}
	l := &lexer{src: []rune(line)}
	elements := []lexedElement{{connector: ConnectorNone}}
	next := func(c Connector) error {
		cur := elements[len(elements)-1]
		if len(cur.words) == 0 {
			return fmt.Errorf("%w: `%s`", ErrEmptySequenceElement, connectorText(cur.connector, c))
		}
		elements = append(elements, lexedElement{connector: c})
		return nil
	}
	for {
		l.skipSeparators()
		if l.eof() {
			break
		}
		cur := &elements[len(elements)-1]
		r := l.peek()
		switch {
		case unquotedLineBreak(r):
			return nil, ErrUnquotedNewline
		case r == ';':
			l.pos++
			if err := next(ConnectorSeq); err != nil {
				return nil, err
			}
		case r == '|':
			l.pos++
			c := ConnectorPipe
			if n, ok := l.peekAt(0); ok && n == '|' {
				l.pos++
				c = ConnectorOr
			} else if ok && n == '&' {
				l.pos++
				c = ConnectorPipeMerged
			}
			if err := next(c); err != nil {
				return nil, err
			}
		case r == '&':
			n, ok := l.peekAt(1)
			switch {
			case ok && n == '&':
				l.pos += 2
				if err := next(ConnectorAnd); err != nil {
					return nil, err
				}
			case ok && n == '>':
				l.pos++
				redirect, err := l.lexRedirect(fdBoth, "&")
				if err != nil {
					return nil, err
				}
				cur.redirects = append(cur.redirects, redirect)
			default:
				return nil, fmt.Errorf("%w: `&`", ErrBackgroundOperator)
			}
		case r == '<' || r == '>':
			redirect, err := l.lexRedirect(fdDefault, "")
			if err != nil {
				return nil, err
			}
			cur.redirects = append(cur.redirects, redirect)
		default:
			word, digits, err := l.lexWord()
			if err != nil {
				return nil, err
			}
			if digits && !l.eof() && (l.peek() == '<' || l.peek() == '>') {
				fd, _ := strconv.Atoi(word.Text)
				redirect, err := l.lexRedirect(fd, word.Text)
				if err != nil {
					return nil, err
				}
				cur.redirects = append(cur.redirects, redirect)
				continue
			}
			cur.words = append(cur.words, word)
		}
	}
	last := elements[len(elements)-1]
	if len(last.words) == 0 {
		if len(elements) == 1 {
			return nil, ErrCommandRequired
		}
		return nil, fmt.Errorf("%w: `%s`", ErrEmptySequenceElement, last.connector)
	}
	return elements, nil
}

func connectorText(previous, next Connector) Connector {
	if previous != ConnectorNone {
		return previous
	}
	return next
}

// lexWord reads one word up to an unquoted separator or operator. digits
// reports a non-empty word made only of unquoted decimal digits, which names a
// file descriptor when a redirection operator follows it directly.
func (l *lexer) lexWord() (Word, bool, error) {
	var text, pattern strings.Builder
	eq := -1
	glob := false
	digits := true
	quotedAny := false
	addressed := false
	literal := func(r rune) {
		text.WriteRune(r)
		pattern.WriteString(EscapeGlob(string(r)))
	}
	for !l.eof() {
		r := l.peek()
		if argSeparator(r) || unquotedLineBreak(r) || operatorRune(r) {
			break
		}
		l.pos++
		switch r {
		case '\'':
			quotedAny, digits = true, false
			closed := false
			for !l.eof() {
				q := l.peek()
				l.pos++
				if q == '\'' {
					closed = true
					break
				}
				literal(q)
			}
			if !closed {
				return Word{}, false, ErrUnterminatedQuote
			}
		case '"':
			quotedAny, digits = true, false
			closed := false
			for !l.eof() {
				q := l.peek()
				l.pos++
				if q == '"' {
					closed = true
					break
				}
				if q == '\\' {
					if l.eof() {
						return Word{}, false, ErrUnterminatedQuote
					}
					e := l.peek()
					l.pos++
					if e != '"' && e != '\\' {
						literal('\\')
					}
					literal(e)
					continue
				}
				literal(q)
			}
			if !closed {
				return Word{}, false, ErrUnterminatedQuote
			}
		case '\\':
			digits = false
			if l.eof() {
				literal('\\')
				continue
			}
			e := l.peek()
			if !strings.ContainsRune(escapableChars, e) {
				literal('\\')
				continue
			}
			l.pos++
			quotedAny = true
			literal(e)
		case '$', '`':
			varName := ""
			if r == '$' {
				var b strings.Builder
				idx := 0
				inBraces := false
				if ch, ok := l.peekAt(0); ok && ch == '{' {
					inBraces = true
					idx = 1
				}
				for {
					ch, ok := l.peekAt(idx)
					if !ok {
						break
					}
					if inBraces && ch == '}' {
						break
					}
					if !((ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || ch == '_' || (idx > 0 && !inBraces && ch >= '0' && ch <= '9') || (idx > 1 && inBraces && ch >= '0' && ch <= '9')) {
						break
					}
					b.WriteRune(ch)
					idx++
				}
				varName = b.String()
			}
			return Word{}, false, &MetacharacterError{Char: string(r), Var: varName}
		default:
			if r < '0' || r > '9' {
				digits = false
			}
			if r == '=' && eq < 0 {
				eq = text.Len()
			}
			if r == '@' && text.Len() == 0 && !quotedAny {
				addressed = true
			}
			if strings.ContainsRune(GlobChars, r) {
				glob = true
			}
			text.WriteRune(r)
			pattern.WriteRune(r)
		}
	}
	word := Word{Text: text.String(), Addressed: addressed, eq: eq}
	if glob {
		word.Pattern = pattern.String()
	}
	if text.Len() == 0 && !quotedAny {
		digits = false
	}
	return word, digits && text.Len() > 0, nil
}

const (
	fdDefault = -2
	fdBoth    = -1
)

// lexRedirect reads one redirection operator and its target. fd is the
// explicit descriptor, fdDefault when none was written, or fdBoth after `&`.
func (l *lexer) lexRedirect(fd int, written string) (Redirect, error) {
	r := l.peek()
	l.pos++
	op := RedirectOp(r)
	written += string(r)
	if r == '<' {
		if n, ok := l.peekAt(0); ok && (n == '<' || n == '&' || n == '>') {
			if n == '<' {
				// Here-documents and here-strings have no structured source here.
				return Redirect{}, &MetacharacterError{Char: "<<"}
			}
			return Redirect{}, &RedirectionError{Issue: IssueOperatorUnsupported, Operator: written + string(n)}
		}
		if fd == fdDefault {
			fd = 0
		}
		if fd != 0 {
			return Redirect{}, &RedirectionError{Issue: IssueDescriptorUnsupported, Operator: written}
		}
		target, err := l.lexTarget()
		if err != nil {
			return Redirect{}, err
		}
		return Redirect{Fd: 0, Op: RedirectIn, Target: target}, nil
	}
	dup := false
	if n, ok := l.peekAt(0); ok {
		switch {
		case n == '>':
			l.pos++
			op, written = RedirectAppend, written+">"
		case n == '&' && fd != fdBoth:
			l.pos++
			dup, written = true, written+"&"
		case n == '|' && fd != fdBoth:
			l.pos++
			written += "|"
		}
	}
	if fd == fdDefault {
		fd = 1
	}
	if fd != 1 && fd != 2 && fd != fdBoth {
		return Redirect{}, &RedirectionError{Issue: IssueDescriptorUnsupported, Operator: written}
	}
	target, err := l.lexTarget()
	if err != nil {
		return Redirect{}, err
	}
	if !dup {
		return Redirect{Fd: fd, Op: op, Target: target}, nil
	}
	switch target {
	case "1", "2":
		return Redirect{Fd: fd, Op: RedirectDup, Target: target}, nil
	case "-":
		return Redirect{}, &RedirectionError{Issue: IssueOperatorUnsupported, Operator: written + target}
	}
	if isDigits(target) {
		return Redirect{}, &RedirectionError{Issue: IssueDescriptorUnsupported, Operator: written + target}
	}
	if fd != 1 {
		return Redirect{}, &RedirectionError{Issue: IssueOperatorUnsupported, Operator: written}
	}
	// `>&file` is the older spelling of `&>file`.
	return Redirect{Fd: fdBoth, Op: RedirectOut, Target: target}, nil
}

// lexTarget reads the word a redirection operator names.
func (l *lexer) lexTarget() (string, error) {
	l.skipSeparators()
	if l.eof() || unquotedLineBreak(l.peek()) || operatorRune(l.peek()) {
		return "", ErrRedirectionTargetRequired
	}
	word, _, err := l.lexWord()
	if err != nil {
		return "", err
	}
	if word.Text == "" {
		return "", ErrRedirectionTargetRequired
	}
	return word.Text, nil
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// scanUnquoted calls visit for every rune outside quotes and escapes, stopping
// when visit returns true.
func scanUnquoted(line string, visit func(runes []rune, i int) bool) {
	runes := []rune(line)
	inQuote := false
	quoteChar := rune(0)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		switch {
		case inQuote:
			if quoteChar == '"' && r == '\\' {
				i++
			} else if r == quoteChar {
				inQuote = false
			}
		case r == '\'' || r == '"':
			inQuote, quoteChar = true, r
		case r == '\\':
			if i+1 < len(runes) && strings.ContainsRune(escapableChars, runes[i+1]) {
				i++
			}
		default:
			if visit(runes, i) {
				return
			}
		}
	}
}
