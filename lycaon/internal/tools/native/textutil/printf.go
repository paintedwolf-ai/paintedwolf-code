package textutil

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/toolcontract"
)

// runPrintf validates the whole format, then applies it repeatedly until
// every argument is consumed. Missing numbers are 0 and missing text empty;
// an invalid number is reported, formatted as its valid prefix, status 1.
func runPrintf(inv *invocation) error {
	operands := inv.operands()
	if len(operands) == 0 {
		return inv.usage(inv.commandWord(), "a FORMAT operand")
	}
	format, release, err := parseFormat(inv.ctx, inv.ports.Memory, operands[0].Value)
	if err != nil {
		var invalid formatError
		if errors.As(err, &invalid) {
			return inv.usage(operands[0], err.Error())
		}
		return err
	}
	defer release()
	args := operands[1:].Values()
	workspace := int64(32<<10 + 2*len(operands[0].Value))
	for _, arg := range args {
		workspace = max(workspace, int64(32<<10+2*len(operands[0].Value)+2*len(arg)))
	}
	if err := inv.reserve(workspace); err != nil {
		return err
	}
	for {
		consumed, stopped, err := format.apply(inv, inv.out, args)
		if err != nil {
			return err
		}
		args = args[consumed:]
		if stopped || len(args) == 0 || consumed == 0 {
			return nil
		}
	}
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

func parseFormat(ctx context.Context, memory *toolcontract.ProgramMemory, text string) (out printfFormat, release func(), err error) {
	// Every percent can introduce a conversion and adjacent literal. Reserve
	// the complete directive capacity and literal-buffer overlap before allocating.
	slots := 2*strings.Count(text, "%") + 1
	bound := int64(96*slots + 4*len(text) + 1024)
	if err := memory.ReserveBytes(ctx, bound); err != nil {
		return nil, nil, err
	}
	defer func() {
		if err != nil {
			memory.ReleaseBytes(bound)
		}
	}()
	out = make(printfFormat, 0, slots)
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
			return nil, nil, formatError("a complete % conversion")
		}
		d.verb = text[j]
		if d.verb == '%' && j == i+1 {
			literal.WriteByte('%')
			i = j
			continue
		}
		if strings.IndexByte("sbcdiouxXfFeEgG", d.verb) < 0 {
			return nil, nil, formatError("a %s %b %c %d %i %o %u %x %X %f %F %e %E %g %G or %% conversion")
		}
		flush()
		out = append(out, d)
		i = j
	}
	flush()
	actual := int64(96*cap(out) + 2*len(text))
	memory.ReleaseBytes(bound - actual)
	return out, func() { memory.ReleaseBytes(actual) }, nil
}

// apply writes one pass of the format and reports the arguments it consumed.
func (f printfFormat) apply(inv *invocation, out io.Writer, args []string) (consumed int, stopped bool, err error) {
	numbers := printfNumbers{inv: inv}
	next := func() string {
		if consumed < len(args) {
			consumed++
			return args[consumed-1]
		}
		return ""
	}
	for _, d := range f {
		if d.verb == 0 {
			var literal bytes.Buffer
			decodeEscapes(&literal, d.literal, escapeFormat)
			_, err = out.Write(literal.Bytes())
		} else {
			arg := next()
			switch d.verb {
			case 's':
				err = pad(out, d, arg)
			case 'b':
				var decoded bytes.Buffer
				stopped = decodeEscapes(&decoded, arg, escapeEcho)
				err = pad(out, d, decoded.String())
			case 'c':
				if arg != "" {
					arg = arg[:1]
				}
				err = pad(out, d, arg)
			case 'd', 'i':
				err = writeNumber(inv, out, d, "d", numbers.integer(arg))
			case 'o', 'u', 'x', 'X':
				verb := map[byte]string{'o': "o", 'u': "d", 'x': "x", 'X': "X"}[d.verb]
				err = writeNumber(inv, out, d, verb, uint64(numbers.integer(arg)))
			default:
				if d.precision == "" {
					d.precision = ".6"
				}
				err = writeNumber(inv, out, d, string(d.verb), numbers.float(arg))
			}
		}
		if err != nil || stopped {
			return consumed, stopped, err
		}
	}
	return consumed, false, nil
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
