package sedscript

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
)

// Record is one input record; Text is valid until the next call to Next.
// Source names its input for F ("-" for standard input).
type Record struct {
	Text       []byte
	Terminated bool
	Source     string
}

// Input yields records and io.EOF at the end.
type Input interface {
	Next() (Record, error)
}

// Config binds a program to one run. Writers holds every Program.WriteFiles
// entry; Files holds the contents of each readable r and R file (an absent
// file reads as nothing, as in GNU sed). SpaceLimit bounds pattern and hold
// space; StepLimit bounds branches and D restarts between input reads.
type Config struct {
	Quiet      bool
	Delimiter  byte
	SpaceLimit int64
	StepLimit  int64
	Reserve    func(int64) error
	Stdout     io.Writer
	Stderr     io.Writer
	Writers    map[string]io.Writer
	Files      map[string][]byte
}

// Machine is one sed run. Hold space, the last regex, R file positions and
// quit state persist across the inputs given to Stream.
type Machine struct {
	ctx     context.Context
	program *Program
	config  Config
	outputs map[string]*output
	stream  inputState
	space   spaces
	appends []appendItem
	last    Regex
	cursors map[string]int
	status  int
	quit    bool
}

// inputState is the current input: the record read ahead (so $ is known),
// the line number and the output the pattern space goes to.
type inputState struct {
	in         Input
	out        *output
	ahead      Record
	aheadOK    bool
	line       int
	source     string
	terminated bool
	replaced   bool
	steps      int64
}

// spaces are pattern and hold space and the memory reserved for them.
type spaces struct {
	pattern, hold []byte
	reserved      int64
}

// output is one destination; pending records that the last record written
// lacked its newline, which later output restores first.
type output struct {
	w       io.Writer
	pending bool
}

type appendItem struct {
	text []byte
	file string
}

type ending uint8

const (
	endCycle ending = iota
	endDelete
	endRestart
	endQuit
	endQuitSilent
)

// NewMachine binds the program's writers and files.
func NewMachine(ctx context.Context, program *Program, config Config) (*Machine, error) {
	if config.Stdout == nil {
		return nil, errors.New("sed needs standard output")
	}
	if config.Stderr == nil {
		config.Stderr = io.Discard
	}
	m := &Machine{ctx: ctx, program: program, config: config, outputs: map[string]*output{}, cursors: map[string]int{}}
	m.outputs[Stdout] = &output{w: config.Stdout}
	m.outputs[Stderr] = &output{w: config.Stderr}
	for _, name := range program.writes {
		w, ok := config.Writers[name]
		if !ok {
			return nil, fmt.Errorf("sed write file %s is not open", name)
		}
		m.outputs[name] = &output{w: w}
	}
	return m, nil
}

// Status is the exit code a q or Q command set.
func (m *Machine) Status() int { return m.status }

// Stream runs records through the script, writing the pattern space to out
// (standard output when nil). Line numbers, $ and ranges are per call, so
// sed -s and -i call it once per file. It reports whether q or Q ended the run.
func (m *Machine) Stream(in Input, out io.Writer) (bool, error) {
	if m.quit {
		return true, nil
	}
	m.stream = inputState{in: in, out: m.outputs[Stdout]}
	if out != nil {
		m.stream.out = &output{w: out}
	}
	for _, c := range m.program.commands {
		c.span = rangeState{active: c.first != nil && c.first.kind == addrZero}
	}
	clear(m.cursors)
	if err := m.stream.fill(); err != nil {
		return false, err
	}
	restart := false
	for {
		if !restart {
			if ok, err := m.next(false); err != nil || !ok {
				return false, err
			}
		}
		end, err := m.execute()
		if err != nil {
			return false, err
		}
		if (end == endCycle || end == endQuit) && !m.config.Quiet {
			if err := m.stream.out.emit(m.space.pattern, m.config.Delimiter, m.stream.terminated); err != nil {
				return false, err
			}
		}
		if end != endQuitSilent {
			if err := m.dumpAppends(); err != nil {
				return false, err
			}
		}
		if end == endQuit || end == endQuitSilent {
			m.quit = true
			return true, nil
		}
		restart = end == endRestart
		if restart {
			if err := m.stream.step(m.ctx, m.config.StepLimit); err != nil {
				return false, err
			}
		}
	}
}

// fill reads the next record ahead.
func (s *inputState) fill() error {
	record, err := s.in.Next()
	if errors.Is(err, io.EOF) {
		s.aheadOK = false
		return nil
	}
	if err != nil {
		return err
	}
	s.ahead, s.aheadOK = record, true
	return nil
}

// next makes the record read ahead the pattern space, or with appending adds
// it after a newline (N). Pending appends are written first.
func (m *Machine) next(appending bool) (bool, error) {
	if !m.stream.aheadOK {
		return false, nil
	}
	if err := m.dumpAppends(); err != nil {
		return false, err
	}
	record := m.stream.ahead
	if appending {
		m.space.pattern = append(append(m.space.pattern, m.config.Delimiter), record.Text...)
	} else {
		m.space.pattern = append(m.space.pattern[:0], record.Text...)
	}
	m.stream.terminated, m.stream.source = record.Terminated, record.Source
	m.stream.line++
	m.stream.replaced, m.stream.steps = false, 0
	if err := m.space.account(m.config.SpaceLimit, m.config.Reserve); err != nil {
		return false, err
	}
	return true, m.stream.fill()
}

func (s *inputState) lastLine() bool { return !s.aheadOK }

// step charges one branch or restart that read no input.
func (s *inputState) step(ctx context.Context, limit int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.steps++
	if limit > 0 && s.steps > limit {
		return &LimitError{Resource: "query_work_steps", Maximum: limit, Observed: s.steps}
	}
	return nil
}

// account bounds pattern and hold space and reserves their growth.
func (s *spaces) account(limit int64, reserve func(int64) error) error {
	for _, space := range [][]byte{s.pattern, s.hold} {
		if limit > 0 && int64(len(space)) > limit {
			return &LimitError{Resource: "record_bytes", Maximum: limit, Observed: int64(len(space))}
		}
	}
	total := int64(cap(s.pattern) + cap(s.hold))
	if total <= s.reserved || reserve == nil {
		return nil
	}
	if err := reserve(total - s.reserved); err != nil {
		return err
	}
	s.reserved = total
	return nil
}

// emit writes one record to an output. A record that lacked its delimiter is
// written without it; later output first restores a newline.
func (o *output) emit(record []byte, delimiter byte, terminated bool) error {
	if err := o.raw(record); err != nil {
		return err
	}
	if !terminated {
		o.pending = true
		return nil
	}
	_, err := o.w.Write([]byte{delimiter})
	return err
}

// raw writes bytes after restoring a missing newline.
func (o *output) raw(text []byte) error {
	if o.pending {
		if _, err := o.w.Write([]byte{'\n'}); err != nil {
			return err
		}
		o.pending = false
	}
	_, err := o.w.Write(text)
	return err
}

// text writes a, i and c text and its newline.
func (o *output) text(text []byte) error {
	if err := o.raw(text); err != nil {
		return err
	}
	_, err := o.w.Write([]byte{'\n'})
	return err
}

// dumpAppends writes queued a text, r file contents and R lines.
func (m *Machine) dumpAppends() error {
	for _, item := range m.appends {
		var err error
		if item.file != "" {
			err = m.stream.out.raw(m.config.Files[item.file])
		} else {
			err = m.stream.out.text(item.text)
		}
		if err != nil {
			return err
		}
	}
	m.appends = m.appends[:0]
	return nil
}

// readLine returns the next line of an R file, without its newline.
func (m *Machine) readLine(file string) ([]byte, bool) {
	content, at := m.config.Files[file], m.cursors[file]
	if at >= len(content) {
		return nil, false
	}
	end := bytes.IndexByte(content[at:], '\n')
	if end < 0 {
		end = len(content) - at
		m.cursors[file] = len(content)
	} else {
		m.cursors[file] = at + end + 1
	}
	return content[at : at+end], true
}
