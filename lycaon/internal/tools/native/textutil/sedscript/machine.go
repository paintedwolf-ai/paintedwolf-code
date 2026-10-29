package sedscript

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/lycaon/lycaon/internal/toolcontract"
)

// Record is one input record; Text is valid until the next call to Next.
// Source names its input for F ("-" for standard input).
type Record struct {
	Text       []byte
	Terminated bool
	Source     string
	Pin        *toolcontract.SourcePin
	Offset     int64
}

// Input yields records and io.EOF at the end.
type Input interface {
	Next() (Record, error)
}

// Config binds a program to one run. Writers holds every Program.WriteFiles
// entry; OpenFile opens an r or R input when reached (nil for an absent
// or unreadable file). SpaceLimit bounds pattern and hold
// space; StepLimit bounds branches and D restarts between input reads.
type Config struct {
	Quiet        bool
	Delimiter    byte
	SpaceLimit   int64
	StepLimit    int64
	Reserve      func(int64) error
	Stdout       io.Writer
	WriteLiteral func(pin *toolcontract.SourcePin, p []byte, offset int64) error
	Stderr       io.Writer
	Writers      map[string]io.Writer
	OpenFile     func(string) (ReadFile, error)
}

// Machine is one sed run. Hold space, the last regex and quit state persist
// across calls to Stream; R file positions restart with each stream.
type Machine struct {
	ctx     context.Context
	program *Program
	config  Config
	outputs map[string]*output
	stream  inputState
	space   spaces
	files   readFiles
	appends []appendItem
	listing []byte
	last    Regex
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
	pin        *toolcontract.SourcePin
	offset     int64
	terminated bool
	replaced   bool
	steps      int64
}

// output is one destination; pending records that the last record written
// lacked its newline, which later output restores first.
type output struct {
	w            io.Writer
	writeLiteral func(pin *toolcontract.SourcePin, p []byte, offset int64) error
	pending      bool
}

type appendItem struct {
	text []byte
	file string
	line bool
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
	if len(program.reads) > 0 {
		if config.OpenFile == nil {
			return nil, errors.New("sed read files need a stream factory")
		}
		if config.Reserve != nil {
			if err := config.Reserve(int64(len(program.reads)) * 256); err != nil {
				return nil, err
			}
		}
	}
	m := &Machine{ctx: ctx, program: program, config: config, space: spaces{limit: config.SpaceLimit, reserve: config.Reserve}, outputs: map[string]*output{}, files: readFiles{open: config.OpenFile, reserve: config.Reserve, cursors: map[string]ReadFile{}}}
	m.outputs[Stdout] = &output{w: config.Stdout, writeLiteral: config.WriteLiteral}
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
func (m *Machine) Stream(in Input, out io.Writer) (_ bool, finalErr error) {
	if m.quit {
		return true, nil
	}
	if err := m.files.start(); err != nil {
		return false, err
	}
	defer func() {
		if err := m.files.close(); err != nil {
			if finalErr == nil {
				finalErr = err
			} else {
				finalErr = errors.Join(finalErr, err)
			}
		}
	}()
	m.stream = inputState{in: in, out: m.outputs[Stdout]}
	if out != nil {
		m.stream.out = &output{w: out}
	}
	for _, c := range m.program.commands {
		c.span = rangeState{active: c.first != nil && c.first.kind == addrZero}
	}
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
			if err := m.stream.out.emitRecord(m.space.pattern, m.config.Delimiter, m.stream.terminated, m.stream.pin, m.stream.offset); err != nil {
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
	var err error
	if appending {
		err = m.space.join(&m.space.pattern, m.config.Delimiter, record.Text)
		m.stream.pin = nil
	} else {
		err = m.space.assign(&m.space.pattern, record.Text)
		m.stream.pin, m.stream.offset = record.Pin, record.Offset
	}
	if err != nil {
		return false, err
	}
	m.stream.terminated, m.stream.source = record.Terminated, record.Source
	m.stream.line++
	m.stream.replaced, m.stream.steps = false, 0
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

// emitRecord writes one record to an output, recording a source mapping when
// the record is unmodified and the output accepts literal mappings.
func (o *output) emitRecord(record []byte, delimiter byte, terminated bool, pin *toolcontract.SourcePin, offset int64) error {
	if o.writeLiteral == nil || pin == nil {
		return o.emit(record, delimiter, terminated)
	}
	if o.pending {
		if _, err := o.w.Write([]byte{'\n'}); err != nil {
			return err
		}
		o.pending = false
	}
	if !terminated {
		o.pending = true
		return o.writeLiteral(pin, record, offset)
	}
	if err := o.writeLiteral(pin, record, offset); err != nil {
		return err
	}
	return o.writeLiteral(pin, []byte{delimiter}, offset+int64(len(record)))
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
			err = m.files.append(item, m.stream.out)
		} else {
			err = m.stream.out.text(item.text)
		}
		if err != nil {
			return err
		}
	}
	clear(m.appends)
	m.appends = m.appends[:0]
	return nil
}
