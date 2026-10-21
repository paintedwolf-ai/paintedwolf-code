package literal

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
)

const hexDigits = "0123456789abcdef"

// byteRange selects [start, start+length) after -s/-l (xxd) or -s/-n (hexdump).
func (inv *invocation) byteRange(size int64, lengthKey string, negative bool) (int64, int64, error) {
	start := int64(0)
	if value, ok := inv.value("-s"); ok {
		n, err := parseOffset(value)
		if err != nil || (n < 0 && !negative) {
			return 0, 0, inv.usage(value, "offset")
		}
		if n < 0 {
			n = max(size+n, 0)
		}
		start = min(n, size)
	}
	end := size
	if value, ok := inv.value(lengthKey); ok {
		n, err := parseOffset(value)
		if err != nil || n < 0 {
			return 0, 0, inv.usage(value, "nonnegative byte count")
		}
		end = min(start+n, size)
	}
	return start, end, nil
}

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

// singleInput gathers the one optional PATH (or stdin) a dump reads.
func (inv *invocation) singleInput() (*input, error) {
	operands := inv.operandsOrStdin()
	if len(operands) > 1 {
		return nil, inv.usage(operands[1], "at most one PATH")
	}
	return inv.open(operands[0], true)
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
	if _, ok := inv.value("-s"); ok && in.stdin {
		if value, _ := inv.value("-s"); strings.HasPrefix(value, "-") {
			return inv.usage(value, "negative offset requires a captured PATH")
		}
	}
	start, end, err := inv.byteRange(int64(len(in.data)), "-l", true)
	if err != nil {
		return err
	}
	data := in.data[start:end]
	if inv.flag("-p") {
		var b strings.Builder
		for i, c := range data {
			b.WriteByte(hexDigits[c>>4])
			b.WriteByte(hexDigits[c&15])
			if (i+1)%30 == 0 || i == len(data)-1 {
				b.WriteByte('\n')
			}
		}
		return inv.out.text(b.String())
	}
	const columns = 16
	width := 2*columns + columns/int(group) - 1
	for row := 0; row < len(data); row += columns {
		chunk := data[row:min(row+columns, len(data))]
		var line strings.Builder
		fmt.Fprintf(&line, "%08x: ", start+int64(row))
		for i, c := range chunk {
			line.WriteByte(hexDigits[c>>4])
			line.WriteByte(hexDigits[c&15])
			if (i+1)%int(group) == 0 && i+1 < columns {
				line.WriteByte(' ')
			}
		}
		pad := 10 + width - line.Len()
		line.WriteString(strings.Repeat(" ", max(pad, 0)) + "  ")
		for _, c := range chunk {
			line.WriteByte(printable(c))
		}
		line.WriteByte('\n')
		if err := inv.out.text(line.String()); err != nil {
			return err
		}
	}
	return nil
}

func runHexdump(inv *invocation) error {
	if !inv.flag("-C") {
		return inv.usage("hexdump", "-C canonical display")
	}
	var data []byte
	for _, operand := range inv.operandsOrStdin() {
		in, err := inv.open(operand, true)
		if err != nil {
			return err
		}
		if in != nil {
			data = append(data, in.data...)
		}
	}
	start, end, err := inv.byteRange(int64(len(data)), "-n", false)
	if err != nil {
		return err
	}
	var previous []byte
	starred := false
	for offset := start; offset < end; offset += 16 {
		chunk := data[offset:min(offset+16, end)]
		if len(chunk) == 16 && bytes.Equal(chunk, previous) {
			if !starred {
				if err := inv.out.text("*\n"); err != nil {
					return err
				}
				starred = true
			}
			continue
		}
		previous, starred = chunk, false
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
	}
	if end > start {
		return inv.out.text(fmt.Sprintf("%08x\n", end))
	}
	return nil
}
