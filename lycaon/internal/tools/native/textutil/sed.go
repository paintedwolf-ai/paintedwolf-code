package textutil

import (
	"bytes"
	"errors"
	"io"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/textsearch"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools/native/nativeutility"
)

// runSed parses the complete script before any input is read, then runs one
// cycle per input record across every input in order, or edits each file in
// place with -i (GNU -i and BSD -i ”; a backup suffix refuses at binding).
// Output preserves a final record's missing newline.
func runSed(inv *invocation) error {
	if inv.owners.Regex == nil {
		return toolrejection.RejectInvalidArguments("COMMAND_PLATFORM_UNAVAILABLE", map[string]any{"command": "sed", "capability": "the shared POSIX regex engine"})
	}
	script, scriptWord, files, err := sedScript(inv)
	if err != nil {
		return err
	}
	commands, err := parseSed(inv.ctx, script, inv.flag("-E"), inv.owners.Regex)
	if err != nil {
		return sedRefusal(inv, scriptWord, err)
	}
	if _, inPlace := inv.argv.Last("-i"); inPlace {
		return sedInPlace(inv, commands, files)
	}
	sources, err := inv.openInputs(files)
	if err != nil {
		return err
	}
	defer closeSources(sources)
	readers := make([]io.Reader, len(sources))
	for i, source := range sources {
		readers[i] = source.reader
	}
	records, err := inv.records(io.MultiReader(readers...), '\n')
	if err != nil {
		return err
	}
	run := &sedRun{inv: inv, commands: commands, quiet: inv.flag("-n"), records: records, out: inv.out}
	_, err = run.cycles()
	return regexBudget(err)
}

// regexBudget reports the shared engine's metering stop as its catalog limit.
func regexBudget(err error) error {
	var budget *textsearch.BudgetError
	if errors.As(err, &budget) {
		return errors.Join(err, nativeutility.LimitExceeded(budget.Resource, budget.Maximum, budget.Observed))
	}
	return err
}

// sedScript joins -e scripts with newlines, or takes the first operand.
func sedScript(inv *invocation) (string, commandprogram.Field, commandprogram.Fields, error) {
	scripts := inv.argv.Options("-e")
	operands := inv.operands()
	if len(scripts) == 0 {
		if len(operands) == 0 {
			return "", commandprogram.Field{}, nil, inv.usage(inv.commandWord(), "a SCRIPT")
		}
		return operands[0].Value, operands[0], operands[1:], nil
	}
	parts := make([]string, len(scripts))
	for i, option := range scripts {
		parts[i] = option.Values[0].Value
	}
	return strings.Join(parts, "\n"), scripts[0].Values[0], operands, nil
}

func sedRefusal(inv *invocation, word commandprogram.Field, err error) error {
	var script *sedScriptError
	if errors.As(err, &script) {
		if script.unsupported {
			// The catalog refuses script commands outside it as unsupported options.
			return toolrejection.RejectInvalidArguments("COMMAND_OPTION_UNSUPPORTED", map[string]any{
				"command": "sed", "word": word.Value, "expected": script.Error(), "expected_form": inv.spec.Argv,
				"source_start_byte": word.Span.StartByte, "source_end_byte": word.Span.EndByte, "source_line": word.Span.Line, "source_column": word.Span.Column,
			})
		}
		return inv.usage(word, script.Error())
	}
	var regex *sedRegexError
	if errors.As(err, &regex) {
		return errors.Join(err, inv.usage(word, "a valid regex at script byte "+strconv.Itoa(regex.offset)))
	}
	return err
}

// sedRun is one execution: the pattern space, the pending append queue and
// the missing-newline state of the last output.
type sedRun struct {
	inv      *invocation
	commands []*sedCommand
	quiet    bool
	records  *records
	out      io.Writer
	line     int
	last     bool
	newline  bool // current record ended with a newline
	pending  bool // the last output record omitted its newline
	hold     []byte
	appends  []string
	// ahead is the next record, read early so $ is known.
	ahead sedRecord
}

type sedRecord struct {
	text       []byte
	terminated bool
	ok         bool
}

func (r *sedRun) read() (sedRecord, error) {
	record, terminated, err := r.records.nextTerminated()
	if errors.Is(err, io.EOF) {
		return sedRecord{}, nil
	}
	if err != nil {
		return sedRecord{}, err
	}
	return sedRecord{text: bytes.Clone(record), terminated: terminated, ok: true}, nil
}

// cycles runs every record and reports whether q ended the run.
func (r *sedRun) cycles() (bool, error) {
	var err error
	if r.ahead, err = r.read(); err != nil {
		return false, err
	}
	for r.ahead.ok {
		space, terminated := r.ahead.text, r.ahead.terminated
		if err := r.inv.reserve(int64(len(space))); err != nil {
			return false, err
		}
		if r.ahead, err = r.read(); err != nil {
			return false, err
		}
		r.line++
		r.last, r.newline = !r.ahead.ok, terminated
		quit, err := r.cycle(space)
		if err != nil || quit {
			return quit, err
		}
	}
	return false, nil
}

// cycle runs every command on one pattern space and reports a quit.
func (r *sedRun) cycle(space []byte) (bool, error) {
	r.appends = r.appends[:0]
	deleted, quit := false, false
	for i := 0; i < len(r.commands) && !deleted && !quit; i++ {
		c := r.commands[i]
		selected, ends, err := r.selects(c, space)
		if err != nil {
			return false, err
		}
		if c.name == '{' {
			if !selected {
				i = c.block - 1
			}
			continue
		}
		if !selected || c.name == '}' {
			continue
		}
		switch c.name {
		case 'p':
			err = r.emit(space, r.newline)
		case 'd':
			deleted = true
		case 'q':
			quit = true
		case 'a':
			r.appends = append(r.appends, c.text)
		case 'i':
			err = r.emit([]byte(c.text), true)
		case 'c':
			deleted = true
			if ends || c.negate {
				err = r.emit([]byte(c.text), true)
			}
		case 's':
			var printed bool
			space, printed, err = r.substitute(c.sub, space)
			if err == nil && printed {
				err = r.emit(space, r.newline)
			}
		case 'h':
			if err = r.inv.reserve(int64(len(space))); err == nil {
				r.hold = bytes.Clone(space)
			}
		case 'H':
			if err = r.inv.reserve(int64(len(space) + 1)); err == nil {
				r.hold = append(append(r.hold, '\n'), space...)
			}
		case 'g':
			if err = r.inv.reserve(int64(len(r.hold))); err == nil {
				space = bytes.Clone(r.hold)
			}
		case 'G':
			if err = r.inv.reserve(int64(len(r.hold) + 1)); err == nil {
				space = append(append(space, '\n'), r.hold...)
			}
		case 'x':
			space, r.hold = r.hold, space
		}
		if err != nil {
			return false, err
		}
	}
	if !deleted && !r.quiet {
		if err := r.emit(space, r.newline); err != nil {
			return false, err
		}
	}
	for _, text := range r.appends {
		if err := r.emit([]byte(text), true); err != nil {
			return false, err
		}
	}
	return quit, nil
}

// emit writes one output record. A record that lacked its newline is written
// without it; any later output first restores that newline.
func (r *sedRun) emit(record []byte, newline bool) error {
	if r.pending {
		if _, err := r.out.Write([]byte{'\n'}); err != nil {
			return err
		}
		r.pending = false
	}
	if _, err := r.out.Write(record); err != nil {
		return err
	}
	if !newline {
		r.pending = true
		return nil
	}
	_, err := r.out.Write([]byte{'\n'})
	return err
}

// selects applies the command's address and reports whether this line ends
// its selection. A range starts when its first address matches; a regex end
// is searched from the next line; a numeric end at or before the start line
// selects that line alone.
func (r *sedRun) selects(c *sedCommand, space []byte) (selected, ends bool, err error) {
	if c.first == nil {
		return !c.negate, true, nil
	}
	if c.second == nil {
		matched, err := r.matches(c.first, space)
		return matched != c.negate, true, err
	}
	if c.active {
		closes := r.last
		switch {
		case c.second.regex != nil:
			matched, err := r.matches(c.second, space)
			if err != nil {
				return false, false, err
			}
			closes = closes || matched
		case !c.second.last:
			closes = closes || r.line >= c.second.line
		}
		c.active = !closes
		return !c.negate, closes, nil
	}
	matched, err := r.matches(c.first, space)
	if err != nil || !matched {
		return c.negate, true, err
	}
	c.active = !r.last
	if c.second.regex == nil && !c.second.last {
		c.active = c.active && c.second.line > r.line
	}
	return !c.negate, !c.active, nil
}

func (r *sedRun) matches(a *sedAddress, space []byte) (bool, error) {
	switch {
	case a.last:
		return r.last, nil
	case a.regex != nil:
		match, err := a.regex.Find(r.inv.ctx, space, 0)
		return match != nil, err
	}
	return r.line == a.line, nil
}
