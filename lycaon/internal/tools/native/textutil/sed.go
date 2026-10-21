package textutil

import (
	"bytes"
	"errors"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools/native/nativeutility"
)

// runSed parses the complete script before any input is read, then runs one
// cycle per input record across every input in order. In-place editing is a
// staged publication this frontend refuses; output preserves a final record's
// missing newline.
func runSed(inv *invocation) error {
	if _, inPlace := inv.argv.Last("-i"); inPlace {
		return unsupported(inv, inv.commandWord(), "a redirect; sed -i publication is the mutation owner's")
	}
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
	run := &sedRun{inv: inv, commands: commands, quiet: inv.flag("-n"), records: records}
	return run.cycles()
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
	line     int
	last     bool
	newline  bool // current record ended with a newline
	pending  bool // the last output record omitted its newline
	appends  []string
	next     []byte
	nextLF   bool
	nextOK   bool
}

func (r *sedRun) read() ([]byte, bool, bool, error) {
	record, terminated, err := r.records.nextTerminated()
	if errors.Is(err, io.EOF) {
		return nil, false, false, nil
	}
	if err != nil {
		return nil, false, false, err
	}
	return bytes.Clone(record), terminated, true, nil
}

func (r *sedRun) cycles() error {
	var err error
	if r.next, r.nextLF, r.nextOK, err = r.read(); err != nil {
		return err
	}
	for r.nextOK {
		space, terminated := r.next, r.nextLF
		if err := r.inv.reserve(int64(len(space))); err != nil {
			return err
		}
		if r.next, r.nextLF, r.nextOK, err = r.read(); err != nil {
			return err
		}
		r.line++
		r.last, r.newline = !r.nextOK, terminated
		quit, err := r.cycle(space)
		if err != nil || quit {
			return err
		}
	}
	return nil
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
		if _, err := r.inv.out.Write([]byte{'\n'}); err != nil {
			return err
		}
		r.pending = false
	}
	if _, err := r.inv.out.Write(record); err != nil {
		return err
	}
	if !newline {
		r.pending = true
		return nil
	}
	_, err := r.inv.out.Write([]byte{'\n'})
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

// substitute replaces the selected matches: the ordinal-th only, or with g
// every match from the ordinal-th (default first). An empty match directly
// after a previous match is skipped, as POSIX requires, and the search then
// advances one scalar so it always terminates.
func (r *sedRun) substitute(s *sedSubstitution, space []byte) ([]byte, bool, error) {
	ordinal := max(s.ordinal, 1)
	var out []byte
	copied, from, count, previousEnd := 0, 0, 0, -1
	replaced := false
	for from <= len(space) {
		match, err := s.regex.Find(r.inv.ctx, space, from)
		if err != nil {
			return nil, false, err
		}
		if match == nil {
			break
		}
		start, end := match[0], match[1]
		if start == end && start == previousEnd {
			from = start + scalarWidth(space, start)
			continue
		}
		count++
		if count >= ordinal && (s.global || count == ordinal) {
			out = append(out, space[copied:start]...)
			out = appendReplacement(out, s.replacement, space, match)
			copied, replaced = end, true
			if limit := r.inv.limit("record_bytes"); int64(len(out)) > limit {
				return nil, false, nativeutility.LimitExceeded("record_bytes", limit, int64(len(out)))
			}
			if !s.global {
				break
			}
		}
		previousEnd = end
		if end > start {
			from = end
		} else {
			from = end + scalarWidth(space, end)
		}
	}
	if !replaced {
		return space, false, nil
	}
	out = append(out, space[copied:]...)
	if err := r.inv.reserve(int64(len(out))); err != nil {
		return nil, false, err
	}
	return out, s.print, nil
}

func appendReplacement(out []byte, pieces []sedPiece, space []byte, match []int) []byte {
	for _, piece := range pieces {
		if piece.group < 0 {
			out = append(out, piece.literal...)
			continue
		}
		if start, end := match[2*piece.group], match[2*piece.group+1]; start >= 0 && end >= start {
			out = append(out, space[start:end]...)
		}
	}
	return out
}

func scalarWidth(b []byte, at int) int {
	if at >= len(b) {
		return 1
	}
	_, size := utf8.DecodeRune(b[at:])
	return size
}
