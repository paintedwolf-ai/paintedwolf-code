package literal

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
)

// stringsRun scans one pinned input for runs of at least min printable
// bytes, each written verbatim (and mapped to its capture) on its own line.
// A run is held only until it reaches min; longer runs stream.
type stringsRun struct {
	inv      *invocation
	in       *input
	min      int
	eight    bool
	radix    string
	label    string
	held     []byte
	start    int64
	emitting bool
}

func runStrings(inv *invocation) error {
	minimum, err := inv.integer("-n", 4, false)
	if err != nil {
		return err
	}
	if minimum < 1 {
		return inv.usage(strconv.FormatInt(minimum, 10), "minimum string length of at least 1")
	}
	encoding, _ := inv.value("-e")
	if encoding != "" && encoding != "s" && encoding != "S" {
		return inv.usage(encoding, "-e s (7-bit) or S (8-bit)")
	}
	radix, _ := inv.value("-t")
	if inv.flag("-o") {
		radix = "o"
	}
	switch radix {
	case "", "d", "o", "x":
	default:
		return inv.usage(radix, "-t d, o or x")
	}
	if memory := inv.owners.Revisions.Memory; memory != nil {
		release, err := memory.Reserve(inv.ctx, 64<<10+minimum)
		if err != nil {
			return err
		}
		defer release()
	}
	for _, operand := range inv.operandsOrStdin() {
		in, err := inv.openField(operand, false)
		if err != nil {
			return err
		}
		if in == nil {
			continue
		}
		label := operand.Value
		if in.stdin && operand.Value == "-" {
			label = "{standard input}"
		}
		run := &stringsRun{inv: inv, in: in, min: int(minimum), eight: encoding == "S", radix: radix}
		if inv.flag("-f") {
			run.label = label + ": "
		}
		if err := run.scan(); err != nil {
			return err
		}
	}
	return nil
}

func (r *stringsRun) printable(c byte) bool {
	return c == '\t' || c >= 0x20 && c <= 0x7e || r.eight && c >= 0x80
}

func (r *stringsRun) scan() error {
	reader := bufio.NewReaderSize(r.in.reader(), 64<<10)
	var offset int64
	for {
		chunk, _ := reader.Peek(reader.Buffered())
		if len(chunk) == 0 {
			_, err := reader.Peek(1)
			if err == io.EOF {
				return r.end()
			}
			if err != nil {
				return err
			}
			continue
		}
		for i := 0; i < len(chunk); {
			if !r.printable(chunk[i]) {
				if err := r.end(); err != nil {
					return err
				}
				i++
				continue
			}
			j := i
			for j < len(chunk) && r.printable(chunk[j]) {
				j++
			}
			if err := r.extend(chunk[i:j], offset+int64(i)); err != nil {
				return err
			}
			i = j
		}
		offset += int64(len(chunk))
		if _, err := reader.Discard(len(chunk)); err != nil {
			return err
		}
		if err := r.inv.ctx.Err(); err != nil {
			return err
		}
	}
}

// extend adds printable bytes at offset to the current run.
func (r *stringsRun) extend(p []byte, offset int64) error {
	if r.emitting {
		return r.inv.out.literal(r.in, p, offset)
	}
	if len(r.held) == 0 {
		r.start = offset
	}
	if len(r.held)+len(p) < r.min {
		r.held = append(r.held, p...)
		return nil
	}
	prefix := r.label
	switch r.radix {
	case "d":
		prefix += fmt.Sprintf("%7d ", r.start)
	case "o":
		prefix += fmt.Sprintf("%7o ", r.start)
	case "x":
		prefix += fmt.Sprintf("%7x ", r.start)
	}
	if err := r.inv.out.text(prefix); err != nil {
		return err
	}
	r.emitting = true
	if err := r.inv.out.literal(r.in, r.held, r.start); err != nil {
		return err
	}
	r.held = r.held[:0]
	return r.inv.out.literal(r.in, p, offset)
}

// end closes the current run at a non-printable byte or the input's end.
func (r *stringsRun) end() error {
	r.held = r.held[:0]
	if !r.emitting {
		return nil
	}
	r.emitting = false
	return r.inv.out.text("\n")
}

// stringsFailure is strings' wording for unreadable operands.
func stringsFailure(operand, reason string) string {
	switch reason {
	case "No such file or directory":
		return fmt.Sprintf("'%s': No such file", operand)
	case "Is a directory":
		return fmt.Sprintf("Warning: '%s' is a directory", operand)
	}
	return fmt.Sprintf("'%s': %s", operand, reason)
}
