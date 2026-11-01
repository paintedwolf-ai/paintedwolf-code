package literal

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/commandprogram"
)

// cmpOptions are cmp's selections; limit and skips are byte counts.
type cmpOptions struct {
	silent, list, bytes bool
	limit               int64
	skip                [2]int64
}

// cmpSide is one compared input and the name diagnostics print for it.
type cmpSide struct {
	name   string
	in     *input
	reader *bufio.Reader
}

// runCmp compares two pinned streams byte by byte: status 0 same, 1
// different, 2 trouble. Both inputs stay open while they are read in step;
// neither is gathered.
func runCmp(inv *invocation) error {
	opts, err := cmpSelections(inv)
	if err != nil {
		return err
	}
	inv.comparison = true
	operands := inv.argv.Operands()
	files := [2]commandprogram.Field{operands[0], {Value: "-"}}
	if len(operands) > 1 {
		files[1] = operands[1]
	}
	for i, operand := range operands[min(len(operands), 2):] {
		n, ok := byteCount(operand.Value)
		if !ok {
			return inv.usage(operand.Value, "SKIP byte count")
		}
		opts.skip[i] = n
	}
	if opts.silent {
		// -s reports trouble only through its status.
		inv.stderr = io.Discard
	}
	if memory := inv.owners.Revisions.Memory; memory != nil {
		release, err := memory.Reserve(inv.ctx, 128<<10)
		if err != nil {
			return err
		}
		defer release()
	}
	if err := inv.admitPaths(files[:]); err != nil {
		return err
	}
	run := cmpRun{inv: inv, opts: opts}
	for i, file := range files {
		in, err := inv.openHeld(file)
		if err != nil || in == nil {
			return err
		}
		run.sides[i] = cmpSide{name: file.Value, in: in, reader: bufio.NewReaderSize(in.reader(), 64<<10)}
		if _, err := io.CopyN(io.Discard, run.sides[i].reader, opts.skip[i]); err != nil && !errors.Is(err, io.EOF) {
			return err
		}
	}
	return run.compare()
}

func cmpSelections(inv *invocation) (cmpOptions, error) {
	opts := cmpOptions{silent: inv.flag("-s"), list: inv.flag("-l"), bytes: inv.flag("-b"), limit: math.MaxInt64}
	operands := inv.operands()
	switch {
	case len(operands) == 0:
		return opts, inv.usage("cmp", "FILE1 [FILE2 [SKIP1 [SKIP2]]]")
	case len(operands) > 4:
		return opts, inv.usage(operands[4], "at most FILE1 FILE2 SKIP1 SKIP2")
	case opts.silent && opts.list:
		return opts, inv.usage("-l", "either -s or -l, not both")
	}
	if value, ok := inv.value("-n"); ok {
		n, valid := byteCount(value)
		if !valid {
			return opts, inv.usage(value, "byte count for -n")
		}
		opts.limit = n
	}
	if value, ok := inv.value("-i"); ok {
		first, second, both := strings.Cut(value, ":")
		a, okA := byteCount(first)
		b, okB := a, okA
		if both {
			b, okB = byteCount(second)
		}
		if !okA || !okB {
			return opts, inv.usage(value, "SKIP or SKIP1:SKIP2 byte counts for -i")
		}
		opts.skip = [2]int64{a, b}
	}
	return opts, nil
}

// cmpRun is one comparison of two opened inputs.
type cmpRun struct {
	inv   *invocation
	opts  cmpOptions
	sides [2]cmpSide
}

// compare walks both inputs to the first difference (all of them with -l),
// the limit, or the end of the shorter input.
func (r cmpRun) compare() error {
	inv, sides, opts := r.inv, r.sides, r.opts
	width := r.offsetWidth()
	var compared, lines int64
	var last byte
	for compared < opts.limit {
		if compared%4096 == 0 {
			if err := inv.ctx.Err(); err != nil {
				return err
			}
		}
		a, errA := sides[0].reader.ReadByte()
		b, errB := sides[1].reader.ReadByte()
		if errA != nil && errA != io.EOF {
			return errA
		}
		if errB != nil && errB != io.EOF {
			return errB
		}
		if errA == io.EOF || errB == io.EOF {
			if errA == io.EOF && errB == io.EOF {
				break
			}
			short := sides[0].name
			if errB == io.EOF {
				short = sides[1].name
			}
			inv.status = 1
			_, _ = fmt.Fprintf(inv.stderr, "cmp: EOF on %s%s\n", quoteName(short), eofPosition(compared, lines, last, opts.list))
			return nil
		}
		compared++
		if a != b {
			inv.status = 1
			if opts.silent {
				return nil
			}
			if !opts.list {
				return inv.out.text(cmpDifference(sides, compared, lines+1, a, b, opts.bytes))
			}
			line := fmt.Sprintf("%*d %3o %3o\n", width, compared, a, b)
			if opts.bytes {
				line = fmt.Sprintf("%*d %3o %-4s %3o %s\n", width, compared, a, caretByte(a), b, caretByte(b))
			}
			if err := inv.out.text(line); err != nil {
				return err
			}
		}
		if a == '\n' {
			lines++
		}
		last = a
	}
	return nil
}

func cmpDifference(sides [2]cmpSide, at, line int64, a, b byte, bytes bool) string {
	if bytes {
		return fmt.Sprintf("%s %s differ: byte %d, line %d is %3o %s %3o %s\n", sides[0].name, sides[1].name, at, line, a, caretByte(a), b, caretByte(b))
	}
	return fmt.Sprintf("%s %s differ: char %d, line %d\n", sides[0].name, sides[1].name, at, line)
}

// eofPosition says where the shorter input ended.
func eofPosition(compared, lines int64, last byte, list bool) string {
	switch {
	case compared == 0:
		return " which is empty"
	case list:
		return " after byte " + strconv.FormatInt(compared, 10)
	case last == '\n':
		return fmt.Sprintf(" after byte %d, line %d", compared, lines)
	}
	return fmt.Sprintf(" after byte %d, in line %d", compared, lines+1)
}

// offsetWidth is the -l offset column: wide enough for the largest
// offset the limit and the regular inputs' remaining bytes allow.
func (r cmpRun) offsetWidth() int {
	largest := r.opts.limit
	for i, side := range r.sides {
		if side.in.pin != nil {
			largest = min(largest, max(side.in.pin.EndByte-r.opts.skip[i], 0))
		}
	}
	return len(strconv.FormatInt(largest, 10))
}

// caretByte is the printable byte notation: M- for the high bit, ^ for
// control characters and ^? for DEL.
func caretByte(c byte) string {
	var out []byte
	if c >= 0x80 {
		out = append(out, 'M', '-')
		c -= 0x80
	}
	switch {
	case c < 0x20:
		out = append(out, '^', c+0x40)
	case c == 0x7f:
		out = append(out, '^', '?')
	default:
		out = append(out, c)
	}
	return string(out)
}

// quoteName quotes a file name in diagnostics.
func quoteName(name string) string {
	return "'" + strings.ReplaceAll(name, "'", `'\''`) + "'"
}
