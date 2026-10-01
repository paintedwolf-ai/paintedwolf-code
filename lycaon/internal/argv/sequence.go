package argv

import "errors"

// Connector joins adjacent argv vectors.
type Connector string

// Connector values use their displayed shell spelling.
const (
	// ConnectorNone marks the first element of a sequence.
	ConnectorNone Connector = ""
	// ConnectorPipe wires the previous element's stdout into this element's stdin.
	ConnectorPipe Connector = "|"
	// ConnectorPipeMerged wires the previous element's stdout and stderr into this element's stdin.
	ConnectorPipeMerged Connector = "|&"
	// ConnectorAnd runs this element only when the previous one exited 0.
	ConnectorAnd Connector = "&&"
	// ConnectorOr runs this element only when the previous one exited non-zero.
	ConnectorOr Connector = "||"
	// ConnectorSeq runs this element regardless of the previous exit status.
	ConnectorSeq Connector = ";"
)

// Sequencing reports whether c gates on the previous element's exit status.
func (c Connector) Sequencing() bool {
	return c == ConnectorAnd || c == ConnectorOr || c == ConnectorSeq
}

// OrPipe resolves an omitted stage connector to a pipe.
func (c Connector) OrPipe() Connector {
	if c == ConnectorNone {
		return ConnectorPipe
	}
	return c
}

var (
	// ErrBackgroundOperator reports a bare `&`, which detaches a command rather
	// than sequencing it.
	ErrBackgroundOperator = errors.New("`&` backgrounds a command rather than sequencing it")
	// ErrEmptySequenceElement reports a sequencing operator with no command on one
	// side of it.
	ErrEmptySequenceElement = errors.New("sequencing operator with no command beside it")
)

// SequenceElement is one argv vector and its preceding connector.
type SequenceElement struct {
	Connector Connector
	Env       map[string]string
	Name      string
	Args      []string
	// Globs holds each argument's glob pattern, aligned with Args; nil when no
	// argument carries an unquoted glob character.
	Globs []string
	// Addressed marks each argument that begins with an unquoted `@`, aligned
	// with Args; nil when none does.
	Addressed []bool
	// Redirects are the element's redirections in written order.
	Redirects []Redirect
	// Streams is the descriptor table those redirections produce.
	Streams Streams
}

// SplitSequence parses argv vectors joined by `|`, `|&`, `&&`, `||`, and `;`.
func SplitSequence(line string) ([]SequenceElement, error) {
	lexed, err := lexLine(line)
	if err != nil {
		return nil, err
	}
	elements := make([]SequenceElement, 0, len(lexed))
	for _, raw := range lexed {
		el, err := buildElement(raw)
		if err != nil {
			return nil, err
		}
		elements = append(elements, el)
	}
	return elements, nil
}

// SplitElement parses a line that must hold exactly one argv vector.
func SplitElement(line string) (SequenceElement, error) {
	elements, err := SplitSequence(line)
	if err != nil {
		return SequenceElement{}, err
	}
	if len(elements) != 1 {
		return SequenceElement{}, &MetacharacterError{Char: string(elements[1].Connector)}
	}
	return elements[0], nil
}

// buildElement splits leading assignments from the executable and its arguments.
func buildElement(raw lexedElement) (SequenceElement, error) {
	el := SequenceElement{Connector: raw.connector, Redirects: raw.redirects}
	cmdIdx := -1
	for i, word := range raw.words {
		if word.eq > 0 && IsEnvIdentifier(word.Text[:word.eq]) {
			if el.Env == nil {
				el.Env = make(map[string]string)
			}
			el.Env[word.Text[:word.eq]] = word.Text[word.eq+1:]
			continue
		}
		cmdIdx = i
		break
	}
	if cmdIdx == -1 {
		return SequenceElement{}, ErrEnvAssignmentCommand
	}
	el.Name = raw.words[cmdIdx].Text
	if trimCommandLine(el.Name) == "" {
		return SequenceElement{}, ErrCommandRequired
	}
	argCount := len(raw.words) - cmdIdx - 1
	for _, word := range raw.words[cmdIdx+1:] {
		el.Args = append(el.Args, word.Text)
		if word.Addressed {
			if el.Addressed == nil {
				el.Addressed = make([]bool, argCount)
			}
			el.Addressed[len(el.Args)-1] = true
		}
		if word.Pattern == "" {
			continue
		}
		if el.Globs == nil {
			el.Globs = make([]string, argCount)
		}
		el.Globs[len(el.Args)-1] = word.Pattern
	}
	streams, err := resolveStreams(raw.redirects)
	if err != nil {
		return SequenceElement{}, err
	}
	el.Streams = streams
	return el, nil
}

// HasUnquotedPipe reports a pipe while ignoring `||`.
func HasUnquotedPipe(line string) bool {
	found := false
	scanUnquoted(line, func(runes []rune, i int) bool {
		if runes[i] != '|' {
			return false
		}
		if (i+1 < len(runes) && runes[i+1] == '|') || (i > 0 && runes[i-1] == '|') {
			return false
		}
		found = true
		return true
	})
	return found
}
