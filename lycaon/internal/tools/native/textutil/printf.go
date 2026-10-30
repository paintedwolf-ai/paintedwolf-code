package textutil

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
)

// runPrintf validates the whole format, then applies it repeatedly until
// every argument is consumed. Missing numbers are 0 and missing text empty;
// an invalid number is reported, formatted as its valid prefix, status 1.
func runPrintf(inv *invocation) error {
	operands := inv.operands()
	if len(operands) == 0 {
		return inv.usage(inv.commandWord(), "a FORMAT operand")
	}
	format, err := parseFormat(operands[0].Value)
	if err != nil {
		return inv.usage(operands[0], err.Error())
	}
	args := operands[1:].Values()
	var out bytes.Buffer
	for {
		consumed, stopped := format.apply(inv, &out, args)
		args = args[consumed:]
		if stopped || len(args) == 0 || consumed == 0 {
			break
		}
	}
	if err := inv.reserve(int64(out.Cap())); err != nil {
		return err
	}
	_, err = inv.out.Write(out.Bytes())
	return err
}

// directive is literal text or one conversion of a printf format.
type directive struct {
	literal   string
	flags     string
	width     string
	precision string
	verb      byte
}

type printfFormat []directive

type formatError string

func (e formatError) Error() string { return string(e) }

func parseFormat(text string) (printfFormat, error) {
	var out printfFormat
	var literal bytes.Buffer
	flush := func() {
		if literal.Len() > 0 {
			out = append(out, directive{literal: literal.String()})
			literal.Reset()
		}
	}
	for i := 0; i < len(text); i++ {
		if text[i] != '%' {
			literal.WriteByte(text[i])
			continue
		}
		j := i + 1
		var d directive
		for j < len(text) && strings.IndexByte("-+ #0", text[j]) >= 0 {
			j++
		}
		d.flags = text[i+1 : j]
		start := j
		for j < len(text) && text[j] >= '0' && text[j] <= '9' {
			j++
		}
		d.width = text[start:j]
		if j < len(text) && text[j] == '.' {
			j++
			start = j
			for j < len(text) && text[j] >= '0' && text[j] <= '9' {
				j++
			}
			d.precision = "." + text[start:j]
		}
		if j == len(text) {
			return nil, formatError("a complete % conversion")
		}
		d.verb = text[j]
		if d.verb == '%' && j == i+1 {
			literal.WriteByte('%')
			i = j
			continue
		}
		if strings.IndexByte("sbcdiouxXfFeEgG", d.verb) < 0 {
			return nil, formatError("a %s %b %c %d %i %o %u %x %X %f %F %e %E %g %G or %% conversion")
		}
		flush()
		out = append(out, d)
		i = j
	}
	flush()
	return out, nil
}

// apply writes one pass of the format and reports the arguments it consumed.
func (f printfFormat) apply(inv *invocation, out *bytes.Buffer, args []string) (consumed int, stopped bool) {
	numbers := printfNumbers{inv: inv}
	next := func() (string, bool) {
		if consumed < len(args) {
			consumed++
			return args[consumed-1], true
		}
		return "", false
	}
	for _, d := range f {
		if d.verb == 0 {
			decodeEscapes(out, d.literal, escapeFormat)
			continue
		}
		arg, _ := next()
		switch d.verb {
		case 's':
			pad(out, d, arg)
		case 'b':
			var decoded bytes.Buffer
			stopped = decodeEscapes(&decoded, arg, escapeEcho)
			pad(out, d, decoded.String())
			if stopped {
				return consumed, true
			}
		case 'c':
			if arg != "" {
				arg = arg[:1]
			}
			pad(out, d, arg)
		case 'd', 'i':
			out.WriteString(formatGo(d, "d", numbers.integer(arg)))
		case 'o', 'u', 'x', 'X':
			verb := map[byte]string{'o': "o", 'u': "d", 'x': "x", 'X': "X"}[d.verb]
			out.WriteString(formatGo(d, verb, uint64(numbers.integer(arg))))
		default:
			precision := d.precision
			if precision == "" {
				precision = ".6"
			}
			d.precision = precision
			out.WriteString(formatGo(d, string(d.verb), numbers.float(arg)))
		}
	}
	return consumed, false
}

// pad applies width and precision to text by bytes, never by characters.
func pad(out *bytes.Buffer, d directive, text string) {
	if d.precision != "" {
		if limit, _ := strconv.Atoi(d.precision[1:]); limit < len(text) {
			text = text[:limit]
		}
	}
	width, _ := strconv.Atoi(d.width)
	fill := strings.Repeat(" ", max(width-len(text), 0))
	if strings.Contains(d.flags, "-") {
		out.WriteString(text + fill)
		return
	}
	out.WriteString(fill + text)
}

func formatGo(d directive, verb string, value any) string {
	return fmt.Sprintf("%"+d.flags+d.width+d.precision+verb, value)
}

// printfNumbers converts numeric arguments, reporting invalid ones on the
// invocation's stderr while formatting their valid prefix.
type printfNumbers struct{ inv *invocation }

// integer parses a C integer constant: decimal, 0x hex, 0 octal or 'c.
func (n printfNumbers) integer(arg string) int64 {
	text := strings.TrimLeft(arg, " \t\n")
	if text == "" {
		return 0
	}
	if text[0] == '\'' || text[0] == '"' {
		if len(text) > 1 {
			return int64(text[1])
		}
		return 0
	}
	negative := false
	body := text
	if body[0] == '-' || body[0] == '+' {
		negative = body[0] == '-'
		body = body[1:]
	}
	base := 10
	switch {
	case strings.HasPrefix(body, "0x") || strings.HasPrefix(body, "0X"):
		base, body = 16, body[2:]
	case strings.HasPrefix(body, "0") && len(body) > 1:
		base, body = 8, body[1:]
	}
	value, count := digits(body, base, len(body))
	if count == 0 || count != len(body) {
		n.inv.fail(1, arg, "invalid number")
	}
	if negative {
		return -int64(value)
	}
	return int64(value)
}

func (n printfNumbers) float(arg string) float64 {
	text := strings.TrimSpace(arg)
	if text == "" {
		return 0
	}
	if text[0] == '\'' || text[0] == '"' {
		return float64(n.integer(arg))
	}
	value, err := strconv.ParseFloat(text, 64)
	if err != nil {
		n.inv.fail(1, arg, "invalid number")
	}
	return value
}
