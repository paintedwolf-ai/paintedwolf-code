package literal

import (
	"bytes"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
)

const hexDigits = "0123456789abcdef"

// parseOffset accepts decimal or 0x hexadecimal with an optional sign.
func parseOffset(value string) (int64, error) {
	sign := int64(1)
	switch {
	case strings.HasPrefix(value, "-"):
		sign, value = -1, value[1:]
	case strings.HasPrefix(value, "+"):
		value = value[1:]
	}
	base := 10
	if strings.HasPrefix(value, "0x") || strings.HasPrefix(value, "0X") {
		base, value = 16, value[2:]
	}
	n, err := strconv.ParseInt(value, base, 64)
	return sign * n, err
}

func printable(c byte) byte {
	if c >= 0x20 && c <= 0x7e {
		return c
	}
	return '.'
}

// singleInput streams the one optional PATH (or stdin) a dump reads.
func (inv *invocation) singleInput() (*input, error) {
	operands := inv.operandsOrStdin()
	if len(operands) > 1 {
		return nil, inv.usage(operands[1].Value, "at most one PATH")
	}
	return inv.openField(operands[0], false)
}

func runXxd(inv *invocation) error {
	group := int64(2)
	if value, ok := inv.value("-g"); ok {
		n, err := strconv.ParseInt(value, 10, 64)
		if err != nil || (n != 1 && n != 2 && n != 4 && n != 8) {
			return inv.usage(value, "group size 1, 2, 4 or 8")
		}
		group = n
	}
	in, err := inv.singleInput()
	if err != nil || in == nil {
		return err
	}
	if memory := inv.owners.Revisions.Memory; memory != nil {
		release, err := memory.Reserve(inv.ctx, 64<<10)
		if err != nil {
			return err
		}
		defer release()
	}
	size := int64(math.MaxInt64)
	if in.pin != nil {
		size = in.pin.EndByte
	} else if !in.stdin && in.stream == nil {
		size = int64(len(in.data))
	}
	reader, start, err := inv.dumpRange(in.reader(), size, "-l", !in.stdin)
	if err != nil {
		return err
	}
	columns := 16
	if inv.flag("-p") {
		columns = 30
	}
	var row [30]byte
	for offset := start; ; {
		n, err := io.ReadFull(reader, row[:columns])
		if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
			return err
		}
		if n == 0 {
			return nil
		}
		chunk := row[:n]
		var line strings.Builder
		if inv.flag("-p") {
			for _, c := range chunk {
				line.WriteByte(hexDigits[c>>4])
				line.WriteByte(hexDigits[c&15])
			}
		} else {
			fmt.Fprintf(&line, "%08x: ", offset)
			for i, c := range chunk {
				line.WriteByte(hexDigits[c>>4])
				line.WriteByte(hexDigits[c&15])
				if (i+1)%int(group) == 0 && i+1 < columns {
					line.WriteByte(' ')
				}
			}
			width := 2*columns + columns/int(group) - 1
			line.WriteString(strings.Repeat(" ", max(10+width-line.Len(), 0)) + "  ")
			for _, c := range chunk {
				line.WriteByte(printable(c))
			}
		}
		line.WriteByte('\n')
		if err := inv.out.text(line.String()); err != nil {
			return err
		}
		offset += int64(n)
	}
}

func runHexdump(inv *invocation) error {
	if !inv.flag("-C") {
		return inv.usage("hexdump", "-C canonical display")
	}
	if memory := inv.owners.Revisions.Memory; memory != nil {
		release, err := memory.Reserve(inv.ctx, 64<<10)
		if err != nil {
			return err
		}
		defer release()
	}
	input := &operandSequence{inv: inv, operands: inv.operandsOrStdin()}
	reader, start, err := inv.dumpRange(input, math.MaxInt64, "-n", false)
	if err != nil {
		return err
	}
	var row, previous [16]byte
	hasPrevious, starred := false, false
	offset := start
	for {
		n, err := io.ReadFull(reader, row[:])
		if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
			return err
		}
		if n == 0 {
			break
		}
		chunk := row[:n]
		if n == 16 && hasPrevious && bytes.Equal(chunk, previous[:]) {
			if !starred {
				if err := inv.out.text("*\n"); err != nil {
					return err
				}
				starred = true
			}
			offset += int64(n)
			continue
		}
		previous = row
		hasPrevious = n == 16
		starred = false
		var line strings.Builder
		fmt.Fprintf(&line, "%08x  ", offset)
		for i := 0; i < 16; i++ {
			if i < len(chunk) {
				line.WriteByte(hexDigits[chunk[i]>>4])
				line.WriteByte(hexDigits[chunk[i]&15])
				line.WriteByte(' ')
			} else {
				line.WriteString("   ")
			}
			if i == 7 {
				line.WriteByte(' ')
			}
		}
		line.WriteString(" |")
		for _, c := range chunk {
			line.WriteByte(printable(c))
		}
		line.WriteString("|\n")
		if err := inv.out.text(line.String()); err != nil {
			return err
		}
		offset += int64(n)
	}
	if offset > start {
		return inv.out.text(fmt.Sprintf("%08x\n", offset))
	}
	return nil
}
