package sedscript

import (
	"bytes"
	"strconv"
)

// execute runs the script on the current pattern space and reports how the
// cycle ends.
func (m *Machine) execute() (ending, error) {
	commands := m.program.commands
	for pc := 0; pc < len(commands); {
		c := commands[pc]
		selected, ends, err := m.selects(c)
		if err != nil {
			return endCycle, err
		}
		if c.name == '{' {
			if selected {
				pc++
			} else {
				pc = c.jump
			}
			continue
		}
		pc++
		if !selected {
			continue
		}
		if c.name == 'b' || c.name == 't' || c.name == 'T' {
			if m.branches(c) {
				if err := m.step(); err != nil {
					return endCycle, err
				}
				pc = c.jump
			}
			continue
		}
		end, done, err := m.run(c, ends)
		if err != nil || done {
			return end, err
		}
	}
	return endCycle, nil
}

// branches reports whether b, t or T jumps; t and T reset the flag they test.
func (m *Machine) branches(c *command) bool {
	switch c.name {
	case 't':
		taken := m.stream.replaced
		m.stream.replaced = false
		return taken
	case 'T':
		taken := !m.stream.replaced
		m.stream.replaced = false
		return taken
	}
	return true
}

// run executes one selected command; done reports that it ended the cycle.
func (m *Machine) run(c *command, ends bool) (ending, bool, error) {
	out := m.stream.out
	pattern := m.space.pattern
	var err error
	switch c.name {
	case 'd':
		return endDelete, true, nil
	case 'D':
		i := bytes.IndexByte(pattern, m.config.Delimiter)
		if i < 0 {
			return endDelete, true, nil
		}
		m.space.pattern = append(pattern[:0], pattern[i+1:]...)
		return endRestart, true, nil
	case 'n', 'N':
		if !m.stream.aheadOK {
			return endCycle, true, nil
		}
		if c.name == 'n' && !m.config.Quiet {
			if err := m.emit(out, pattern, m.stream.terminated); err != nil {
				return endCycle, true, err
			}
		}
		_, err = m.next(c.name == 'N')
	case 'q', 'Q':
		m.status = max(c.number, 0)
		if c.name == 'q' {
			return endQuit, true, nil
		}
		return endQuitSilent, true, nil
	case 'c':
		if ends || c.negate {
			err = m.text(out, c.text)
		}
		return endDelete, true, err
	case 'p':
		err = m.emit(out, pattern, m.stream.terminated)
	case 'P', 'W':
		if c.name == 'W' {
			out = m.outputs[c.arg]
		}
		if i := bytes.IndexByte(pattern, m.config.Delimiter); i >= 0 {
			err = m.emit(out, pattern[:i], true)
		} else {
			err = m.emit(out, pattern, m.stream.terminated)
		}
	case 'w':
		err = m.emit(m.outputs[c.arg], pattern, m.stream.terminated)
	case 'a':
		m.appends = append(m.appends, appendItem{text: c.text})
	case 'i':
		err = m.text(out, c.text)
	case 'r':
		m.appends = append(m.appends, appendItem{file: c.arg})
	case 'R':
		if line, ok := m.readLine(c.arg); ok {
			m.appends = append(m.appends, appendItem{text: line})
		}
	case '=':
		err = m.text(out, strconv.AppendInt(nil, int64(m.stream.line), 10))
	case 'F':
		err = m.text(out, []byte(m.stream.source))
	case 'l':
		err = m.raw(out, list(pattern, c.number))
	case 's':
		err = m.substitute(c.sub)
	default:
		err = m.edit(c)
	}
	return endCycle, false, err
}

// edit runs the commands that only change pattern or hold space.
func (m *Machine) edit(c *command) error {
	s := &m.space
	switch c.name {
	case 'g':
		s.pattern = append(s.pattern[:0], s.hold...)
	case 'G':
		s.pattern = append(append(s.pattern, m.config.Delimiter), s.hold...)
	case 'h':
		s.hold = append(s.hold[:0], s.pattern...)
	case 'H':
		s.hold = append(append(s.hold, m.config.Delimiter), s.pattern...)
	case 'x':
		s.pattern, s.hold = s.hold, s.pattern
	case 'z':
		s.pattern = s.pattern[:0]
	case 'y':
		var out []byte
		for text := s.pattern; len(text) > 0; {
			r, size := decodeScalar(text)
			if to, ok := c.translate[r]; ok {
				r = to
			}
			out = appendScalar(out, r)
			text = text[size:]
		}
		s.pattern = out
	default:
		return nil
	}
	return m.account()
}

// selects applies the command's addresses and reports whether this line ends
// its selection. A range starts when its first address matches; a regex end
// is searched from the next line; a numeric end at or before the start line
// selects that line alone; every range ends with the input.
func (m *Machine) selects(c *command) (selected, ends bool, err error) {
	if c.first == nil {
		return !c.negate, true, nil
	}
	if c.second == nil {
		matched, err := m.matches(c.first)
		return matched != c.negate, true, err
	}
	if !c.span.active {
		return m.startRange(c)
	}
	closes := m.lastLine()
	switch second := c.second; second.kind {
	case addrRegex:
		matched, err := m.matches(second)
		if err != nil {
			return false, false, err
		}
		closes = closes || matched
	case addrLine, addrRelative:
		end := second.line
		if second.kind == addrRelative {
			end = c.span.end
		}
		if m.stream.line > end {
			c.span.active = false
			return m.startRange(c)
		}
		closes = closes || m.stream.line == end
	case addrMultiple:
		closes = closes || second.step <= 0 || m.stream.line%second.step == 0
	}
	c.span.active = !closes
	return !c.negate, closes, nil
}

func (m *Machine) startRange(c *command) (bool, bool, error) {
	matched, err := m.matches(c.first)
	if err != nil || !matched {
		return c.negate, true, err
	}
	active := true
	switch second := c.second; second.kind {
	case addrLine:
		active = second.line > m.stream.line
	case addrRelative:
		c.span.end = m.stream.line + second.line
		active = second.line > 0
	case addrMultiple:
		active = second.step > 0 && m.stream.line%second.step != 0
	}
	c.span.active = active && !m.lastLine()
	return !c.negate, !c.span.active, nil
}

func (m *Machine) matches(a *address) (bool, error) {
	line := m.stream.line
	switch a.kind {
	case addrLine:
		return line == a.line, nil
	case addrLast:
		return m.lastLine(), nil
	case addrStep:
		if a.step <= 0 {
			return line == a.line, nil
		}
		return line >= a.line && (line-a.line)%a.step == 0, nil
	case addrRegex:
		regex, err := m.regex(a.regex)
		if err != nil {
			return false, err
		}
		match, err := regex.Find(m.ctx, m.space.pattern, 0)
		return match != nil, err
	}
	return false, nil
}

// regex resolves the empty regex to the last one applied.
func (m *Machine) regex(regex Regex) (Regex, error) {
	if regex == nil {
		if m.last == nil {
			return nil, &RuntimeError{Reason: "no previous regular expression"}
		}
		return m.last, nil
	}
	m.last = regex
	return regex, nil
}

// list renders l output: C escapes, \ooo for other unprintable bytes, lines
// wrapped with a trailing \ at width-1 characters (0 or 1 never wraps,
// absent is 70), and $ at the end.
func list(pattern []byte, width int) []byte {
	if width < 0 {
		width = 70
	}
	var out []byte
	column := 0
	for _, c := range pattern {
		var item []byte
		switch {
		case c == '\\':
			item = []byte(`\\`)
		case listEscapes[c] != 0:
			item = []byte{'\\', listEscapes[c]}
		case c < 0x20 || c >= 0x7f:
			item = []byte{'\\', '0' + c>>6, '0' + c>>3&7, '0' + c&7}
		default:
			item = []byte{c}
		}
		if width > 1 && column+len(item) > width-1 {
			out = append(out, '\\', '\n')
			column = 0
		}
		out = append(out, item...)
		column += len(item)
	}
	return append(out, '$', '\n')
}

var listEscapes = [256]byte{'\a': 'a', '\b': 'b', '\f': 'f', '\n': 'n', '\r': 'r', '\t': 't', '\v': 'v'}
