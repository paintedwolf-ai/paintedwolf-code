package literal

import (
	"errors"
	"io"
	"strconv"
	"strings"
)

// catOptions are cat's presentation switches; -A is -vET and -b overrides -n.
type catOptions struct {
	number, nonblank, squeeze, nonprinting, ends, tabs bool
}

// transforms reports a presentation that rewrites line content. Only literal
// windows and reversible numbering keep exact source mappings.
func (o catOptions) transforms() bool { return o.nonprinting || o.ends || o.tabs }

// lineState carries numbering, squeezing and line position across every
// input of one run: an unterminated last line continues into the next input.
type lineState struct {
	next      int64
	lastBlank bool
	midLine   bool
}

func runCat(inv *invocation) error {
	all := inv.flag("-A")
	opts := catOptions{
		number: inv.flag("-n"), nonblank: inv.flag("-b"), squeeze: inv.flag("-s"),
		nonprinting: all || inv.flag("-v"), ends: all || inv.flag("-E"), tabs: all || inv.flag("-T"),
	}
	state := &lineState{next: 1}
	for _, operand := range inv.operandsOrStdin() {
		in, err := inv.openField(operand, false)
		if err != nil {
			return err
		}
		if in == nil {
			continue
		}
		if err := catInput(inv, in, opts, state); err != nil {
			return err
		}
	}
	return nil
}

func catInput(inv *invocation, in *input, opts catOptions, state *lineState) error {
	if opts == (catOptions{}) {
		if in.stream == nil {
			return inv.out.literal(in, in.data, 0)
		}
		var (
			buf    [lineBufferBytes]byte
			offset int64
		)
		for {
			n, err := in.stream.Read(buf[:])
			if n > 0 {
				if writeErr := inv.out.literal(in, buf[:n], offset); writeErr != nil {
					return writeErr
				}
				offset += int64(n)
			}
			if err != nil {
				if errors.Is(err, io.EOF) {
					return nil
				}
				return err
			}
		}
	}
	return eachLine(in, func(f fragment) error {
		starts := f.starts && !state.midLine
		state.midLine = !f.terminated()
		blank := starts && f.ends && len(f.data) == 1 && f.data[0] == '\n'
		if starts {
			if opts.squeeze && blank && state.lastBlank {
				return nil
			}
			if (opts.nonblank && !blank) || (opts.number && !opts.nonblank) {
				if err := inv.out.text(numberPrefix(state.next, 6, "\t")); err != nil {
					return err
				}
				state.next++
			}
		}
		if f.ends && !state.midLine {
			state.lastBlank = blank
		}
		if !opts.transforms() {
			return inv.out.literal(in, f.data, f.offset)
		}
		if err := inv.out.text(catNotation(f.body(), opts)); err != nil {
			return err
		}
		if !f.terminated() {
			return nil
		}
		if opts.ends {
			if err := inv.out.text("$"); err != nil {
				return err
			}
		}
		return inv.out.text("\n")
	})
}

// numberPrefix right-aligns n in width, as GNU cat and nl print it.
func numberPrefix(n int64, width int, separator string) string {
	digits := strconv.FormatInt(n, 10)
	if pad := width - len(digits); pad > 0 {
		digits = strings.Repeat(" ", pad) + digits
	}
	return digits + separator
}

// catNotation applies -v (^X and M- notation) and -T (^I) to line content.
func catNotation(body []byte, opts catOptions) string {
	var b strings.Builder
	for _, c := range body {
		switch {
		case c == '\t':
			if opts.tabs {
				b.WriteString("^I")
			} else {
				b.WriteByte(c)
			}
		case !opts.nonprinting:
			b.WriteByte(c)
		case c >= 128:
			b.WriteString("M-")
			c -= 128
			switch {
			case c < 32:
				b.WriteByte('^')
				b.WriteByte(c + 64)
			case c == 127:
				b.WriteString("^?")
			default:
				b.WriteByte(c)
			}
		case c < 32:
			b.WriteByte('^')
			b.WriteByte(c + 64)
		case c == 127:
			b.WriteString("^?")
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

func runNl(inv *invocation) error {
	mode := "t"
	if value, ok := inv.value("-b"); ok {
		mode = value
	}
	if mode != "a" && mode != "t" {
		return inv.usage(mode, "line numbering mode a or t")
	}
	start, err := inv.integer("-v", 1, true)
	if err != nil {
		return err
	}
	width, err := inv.integer("-w", 6, false)
	if err != nil {
		return err
	}
	if width < 1 || width > 1<<10 {
		return inv.usage(strconv.FormatInt(width, 10), "width from 1 to 1024")
	}
	separator := "\t"
	if value, ok := inv.value("-s"); ok {
		separator = value
	}
	operands := inv.operandsOrStdin()
	if len(operands) > 1 {
		return inv.usage(operands[1].Value, "at most one PATH")
	}
	in, err := inv.openField(operands[0], false)
	if err != nil || in == nil {
		return err
	}
	blankPrefix := strings.Repeat(" ", int(width)+len(separator))
	next := start
	terminated := true
	err = eachLine(in, func(f fragment) error {
		terminated = f.terminated()
		if f.starts {
			blank := f.ends && len(f.data) == 1 && f.data[0] == '\n'
			prefix := blankPrefix
			if mode == "a" || !blank {
				prefix = numberPrefix(next, int(width), separator)
				next++
			}
			if err := inv.out.text(prefix); err != nil {
				return err
			}
		}
		return inv.out.literal(in, f.data, f.offset)
	})
	if err != nil || terminated {
		return err
	}
	// nl terminates every numbered line it writes.
	return inv.out.text("\n")
}

// integer parses one decimal option value; negative values need allowNegative.
func (inv *invocation) integer(key string, fallback int64, allowNegative bool) (int64, error) {
	value, ok := inv.value(key)
	if !ok {
		return fallback, nil
	}
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil || (!allowNegative && n < 0) {
		return 0, inv.usage(value, "decimal integer for "+key)
	}
	return n, nil
}
