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
		selected, ends, err := c.selects(m)
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
			if m.stream.branches(c) {
				if err := m.stream.step(m.ctx, m.config.StepLimit); err != nil {
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
func (s *inputState) branches(c *command) bool {
	switch c.name {
	case 't':
		taken := s.replaced
		s.replaced = false
		return taken
	case 'T':
		taken := !s.replaced
		s.replaced = false
		return taken
	}
	return true
}

// run executes one selected command; done reports that it ended the cycle.
func (m *Machine) run(c *command, ends bool) (ending, bool, error) {
	out := m.stream.out
	pattern := m.space.pattern
	delimiter := m.config.Delimiter
	var err error
	switch c.name {
	case 'd':
		return endDelete, true, nil
	case 'D':
		i := bytes.IndexByte(pattern, delimiter)
		if i < 0 {
			return endDelete, true, nil
		}
		pattern = append(pattern[:0], pattern[i+1:]...)
		m.space.pattern = pattern
		m.stream.pin = nil
		return endRestart, true, nil
	case 'n', 'N':
		if !m.stream.aheadOK {
			return endCycle, true, nil
		}
		if c.name == 'n' && !m.config.Quiet {
			if err := out.emitRecord(pattern, delimiter, m.stream.terminated, m.stream.pin, m.stream.offset); err != nil {
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
			err = out.text(c.text)
		}
		return endDelete, true, err
	case 'p':
		err = out.emitRecord(pattern, delimiter, m.stream.terminated, m.stream.pin, m.stream.offset)
	case 'P', 'W':
		if c.name == 'W' {
			out = m.outputs[c.arg]
		}
		if i := bytes.IndexByte(pattern, delimiter); i >= 0 {
			err = out.emit(pattern[:i], delimiter, true)
		} else {
			err = out.emitRecord(pattern, delimiter, m.stream.terminated, m.stream.pin, m.stream.offset)
		}
	case 'w':
		err = m.outputs[c.arg].emit(pattern, delimiter, m.stream.terminated)
	case 'a':
		err = m.queueAppend(appendItem{text: c.text})
	case 'i':
		err = out.text(c.text)
	case 'r':
		err = m.queueAppend(appendItem{file: c.arg})
	case 'R':
		err = m.queueAppend(appendItem{file: c.arg, line: true})
	case '=':
		err = out.text(strconv.AppendInt(nil, int64(m.stream.line), 10))
	case 'F':
		err = out.text([]byte(m.stream.source))
	case 'l':
		err = m.list(pattern, c.number)
	case 's':
		err = c.sub.apply(m)
	default:
		switch c.name {
		case 'g', 'G', 'x', 'z', 'y':
			m.stream.pin = nil
		}
		err = m.space.edit(c, delimiter)
	}
	return endCycle, false, err
}

// edit runs the commands that only change pattern or hold space.
func (s *spaces) edit(c *command, delimiter byte) error {
	switch c.name {
	case 'g':
		return s.assign(&s.pattern, s.hold)
	case 'G':
		return s.join(&s.pattern, delimiter, s.hold)
	case 'h':
		return s.assign(&s.hold, s.pattern)
	case 'H':
		return s.join(&s.hold, delimiter, s.pattern)
	case 'x':
		s.pattern, s.hold = s.hold, s.pattern
	case 'z':
		s.pattern = s.pattern[:0]
	case 'y':
		s.scratch = s.scratch[:0]
		for text := s.pattern; len(text) > 0; {
			r, size := decodeScalar(text)
			if to, ok := c.translate[r]; ok {
				r = to
			}
			if err := s.appendRune(r); err != nil {
				return err
			}
			text = text[size:]
		}
		s.swapScratch()
	}
	return nil
}

// selects applies the command's addresses and reports whether this line ends
// its selection. A range starts when its first address matches; a regex end
// is searched from the next line; a numeric end at or before the start line
// selects that line alone; every range ends with the input.
func (c *command) selects(m *Machine) (selected, ends bool, err error) {
	if c.first == nil {
		return !c.negate, true, nil
	}
	if c.second == nil {
		matched, err := c.first.matches(m)
		return matched != c.negate, true, err
	}
	if !c.span.active {
		return c.startRange(m)
	}
	closes := m.stream.lastLine()
	switch second := c.second; second.kind {
	case addrRegex:
		matched, err := second.matches(m)
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
			return c.startRange(m)
		}
		closes = closes || m.stream.line == end
	case addrLast:
		// The shared last-line check above closes this range.
	case addrZero, addrStep:
		// The parser excludes these as second addresses.
	case addrMultiple:
		closes = closes || second.step <= 0 || m.stream.line%second.step == 0
	}
	c.span.active = !closes
	return !c.negate, closes, nil
}

func (c *command) startRange(m *Machine) (bool, bool, error) {
	matched, err := c.first.matches(m)
	if err != nil || !matched {
		return c.negate, true, err
	}
	active := true
	switch second := c.second; second.kind {
	case addrRegex, addrLast:
		// These ranges include their first matching line.
	case addrZero, addrStep:
		// The parser excludes these as second addresses.
	case addrLine:
		active = second.line > m.stream.line
	case addrRelative:
		c.span.end = m.stream.line + second.line
		active = second.line > 0
	case addrMultiple:
		active = second.step > 0 && m.stream.line%second.step != 0
	}
	c.span.active = active && !m.stream.lastLine()
	return !c.negate, !c.span.active, nil
}

func (a *address) matches(m *Machine) (bool, error) {
	line := m.stream.line
	switch a.kind {
	case addrZero, addrRelative, addrMultiple:
		return false, nil
	case addrLine:
		return line == a.line, nil
	case addrLast:
		return m.stream.lastLine(), nil
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
