package textutil

import (
	"bytes"
	"strings"
)

// runEcho joins operands with spaces and ends with LF unless -n. -e enables
// escapes and -E disables them; the last of the two wins. Text is data.
func runEcho(inv *invocation) error {
	escapes := false
	newline := true
	for _, item := range inv.argv.Items {
		if item.Option == nil {
			continue
		}
		switch item.Option.Key {
		case "-n":
			newline = false
		case "-e":
			escapes = true
		case "-E":
			escapes = false
		}
	}
	var out bytes.Buffer
	for i, operand := range inv.operands() {
		if i > 0 {
			out.WriteByte(' ')
		}
		if !escapes {
			out.WriteString(operand.Value)
			continue
		}
		if decodeEscapes(&out, operand.Value, escapeEcho) {
			newline = false
			break
		}
	}
	if newline {
		out.WriteByte('\n')
	}
	if err := inv.reserve(int64(out.Cap())); err != nil {
		return err
	}
	_, err := inv.out.Write(out.Bytes())
	return err
}

type escapeMode uint8

const (
	// escapeEcho is echo -e and printf %b: \0NNN octal and \c stops output.
	escapeEcho escapeMode = iota
	// escapeFormat is a printf format: \NNN octal.
	escapeFormat
)

// decodeEscapes appends text with backslash escapes decoded. It reports a
// \c stop. Unknown escapes keep their backslash, as the utilities do.
func decodeEscapes(out *bytes.Buffer, text string, mode escapeMode) (stopped bool) {
	for i := 0; i < len(text); i++ {
		c := text[i]
		if c != '\\' || i+1 == len(text) {
			out.WriteByte(c)
			continue
		}
		i++
		switch e := text[i]; e {
		case 'a':
			out.WriteByte('\a')
		case 'b':
			out.WriteByte('\b')
		case 'e':
			out.WriteByte(0x1b)
		case 'f':
			out.WriteByte('\f')
		case 'n':
			out.WriteByte('\n')
		case 'r':
			out.WriteByte('\r')
		case 't':
			out.WriteByte('\t')
		case 'v':
			out.WriteByte('\v')
		case '\\':
			out.WriteByte('\\')
		case 'c':
			if mode == escapeEcho {
				return true
			}
			out.WriteString(`\c`)
		case 'x':
			value, n := digits(text[i+1:], 16, 2)
			if n == 0 {
				out.WriteString(`\x`)
				continue
			}
			out.WriteByte(byte(value))
			i += n
		default:
			start, width := i, 3
			if mode == escapeEcho {
				if e != '0' {
					out.WriteByte('\\')
					out.WriteByte(e)
					continue
				}
				start = i + 1
			} else if e < '0' || e > '7' {
				out.WriteByte('\\')
				out.WriteByte(e)
				continue
			}
			value, n := digits(text[start:], 8, width)
			out.WriteByte(byte(value))
			i = start + n - 1
		}
	}
	return false
}

// digits reads up to width digits of base from text.
func digits(text string, base, width int) (int, int) {
	value, n := 0, 0
	for n < width && n < len(text) {
		d := strings.IndexByte("0123456789abcdef"[:base], lower(text[n]))
		if d < 0 {
			break
		}
		value = value*base + d
		n++
	}
	return value, n
}

func lower(c byte) byte {
	if c >= 'A' && c <= 'Z' {
		return c + 'a' - 'A'
	}
	return c
}
