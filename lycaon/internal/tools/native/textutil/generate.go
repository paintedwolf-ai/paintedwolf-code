package textutil

import (
	"bytes"
	"fmt"
	"math/big"
	"strings"

	"github.com/lycaon/lycaon/internal/commandprogram"
)

// runSeq prints FIRST, FIRST+INCREMENT, ... up to LAST in exact decimal
// arithmetic, as GNU seq does: integers print as integers, otherwise every
// number has the precision of the more precise of FIRST and INCREMENT. -w
// pads with leading zeros, -s separates (the final newline is kept), BSD -t
// replaces that final newline, and -f formats each number with one floating
// conversion.
func runSeq(inv *invocation) error {
	operands := inv.operands()
	if len(operands) == 0 || len(operands) > 3 {
		return inv.usage(inv.commandWord(), "LAST, FIRST LAST or FIRST INCREMENT LAST")
	}
	numbers := make([]seqNumber, len(operands))
	for i, operand := range operands {
		n, ok := parseDecimal(operand.Value)
		if !ok {
			return inv.usage(operand, "a decimal number")
		}
		numbers[i] = n
	}
	first, step, last := seqNumber{value: big.NewRat(1, 1)}, seqNumber{value: big.NewRat(1, 1)}, numbers[len(numbers)-1]
	if len(numbers) > 1 {
		first = numbers[0]
	}
	if len(numbers) == 3 {
		step = numbers[1]
		if step.value.Sign() == 0 {
			return inv.usage(operands[1], "a nonzero INCREMENT")
		}
	}
	format, err := seqFormat(inv, first, step, last)
	if err != nil {
		return err
	}
	separator, terminator := "\n", "\n"
	if value, ok := inv.value("-s"); ok {
		separator = value.Value
	}
	if value, ok := inv.value("-t"); ok {
		var decoded bytes.Buffer
		decodeEscapes(&decoded, value.Value, escapeFormat)
		terminator = decoded.String()
	}
	var buffer bytes.Buffer
	count := 0
	for current := new(big.Rat).Set(first.value); step.value.Sign() > 0 && current.Cmp(last.value) <= 0 || step.value.Sign() < 0 && current.Cmp(last.value) >= 0; current.Add(current, step.value) {
		if count > 0 {
			buffer.WriteString(separator)
		}
		buffer.WriteString(format(current))
		count++
		if buffer.Len() >= 32<<10 {
			if err := flushChunk(inv, &buffer); err != nil {
				return err
			}
		}
	}
	if count > 0 {
		buffer.WriteString(terminator)
	}
	return flushChunk(inv, &buffer)
}

// flushChunk writes a bounded chunk of generated output and checks for cancellation.
func flushChunk(inv *invocation, buffer *bytes.Buffer) error {
	if err := inv.ctx.Err(); err != nil {
		return err
	}
	_, err := inv.out.Write(buffer.Bytes())
	buffer.Reset()
	return err
}

type seqNumber struct {
	value     *big.Rat
	precision int
	integer   bool
}

// parseDecimal reads [+-]digits[.digits][e[+-]digits].
func parseDecimal(text string) (seqNumber, bool) {
	body := strings.TrimLeft(text, "+-")
	if len(text)-len(body) > 1 || body == "" {
		return seqNumber{}, false
	}
	mantissa, exponent, hasExponent := strings.Cut(strings.ToLower(body), "e")
	whole, fraction, hasPoint := strings.Cut(mantissa, ".")
	if !allDigits(whole) || !allDigits(fraction) || whole+fraction == "" || (hasExponent && !allDigits(strings.TrimLeft(exponent, "+-"))) || (hasExponent && strings.TrimLeft(exponent, "+-") == "") {
		return seqNumber{}, false
	}
	value, ok := new(big.Rat).SetString(text)
	if !ok {
		return seqNumber{}, false
	}
	n := seqNumber{value: value, integer: !hasPoint && !hasExponent}
	if hasPoint && !hasExponent {
		n.precision = len(fraction)
	}
	return n, true
}

func allDigits(text string) bool {
	for i := 0; i < len(text); i++ {
		if text[i] < '0' || text[i] > '9' {
			return false
		}
	}
	return true
}

func seqFormat(inv *invocation, first, step, last seqNumber) (func(*big.Rat) string, error) {
	if value, ok := inv.value("-f"); ok {
		if inv.flag("-w") {
			return nil, inv.usage(value, "-f or -w, not both")
		}
		if !oneFloatConversion(value.Value) {
			return nil, inv.usage(value, "a FORMAT with exactly one %e, %f, %g, %E, %F, %G, %a or %A conversion")
		}
		return func(v *big.Rat) string {
			f, _ := v.Float64()
			return fmt.Sprintf(value.Value, f)
		}, nil
	}
	precision := max(first.precision, step.precision)
	format := func(v *big.Rat) string { return v.FloatString(precision) }
	if !inv.flag("-w") {
		return format, nil
	}
	width := max(len(format(first.value)), len(format(last.value)))
	return func(v *big.Rat) string {
		text := format(v)
		digits := strings.TrimPrefix(text, "-")
		padding := strings.Repeat("0", max(width-len(text), 0))
		if digits != text {
			return "-" + padding + digits
		}
		return padding + text
	}, nil
}

// oneFloatConversion admits literal text, %% and one conversion with flags,
// width and precision.
func oneFloatConversion(format string) bool {
	conversions := 0
	for i := 0; i < len(format); i++ {
		if format[i] != '%' {
			continue
		}
		i++
		if i < len(format) && format[i] == '%' {
			continue
		}
		for i < len(format) && strings.IndexByte("-+ #0'", format[i]) >= 0 {
			i++
		}
		for i < len(format) && (format[i] >= '0' && format[i] <= '9' || format[i] == '.') {
			i++
		}
		if i >= len(format) || strings.IndexByte("efgEFGaA", format[i]) < 0 {
			return false
		}
		conversions++
	}
	return conversions == 1
}

// runYes repeats its operands (default y) as lines until the consumer closes
// the stream; stream_bytes ends it when nothing does.
func runYes(inv *invocation) error {
	line := "y"
	if operands := inv.operands(); len(operands) > 0 {
		line = strings.Join(operands.Values(), " ")
	}
	chunk := strings.Repeat(line+"\n", max(1, (16<<10)/(len(line)+1)))
	if err := inv.reserve(int64(len(chunk))); err != nil {
		return err
	}
	for {
		if err := inv.ctx.Err(); err != nil {
			return err
		}
		if _, err := inv.out.Write([]byte(chunk)); err != nil {
			return err
		}
	}
}

// numericOperands lets seq take negative numbers as GNU seq does: the first
// word that is a number ends its options.
func numericOperands(command string, argv commandprogram.Fields) commandprogram.Fields {
	if command != "seq" {
		return argv
	}
	for i := 1; i < len(argv); i++ {
		word := argv[i].Value
		if word == "--" {
			return argv
		}
		if _, ok := parseDecimal(word); ok {
			out := append(commandprogram.Fields{}, argv[:i]...)
			out = append(out, commandprogram.Field{Value: "--", Span: argv[i].Span})
			return append(out, argv[i:]...)
		}
		switch word {
		case "-s", "-f", "--separator", "--format":
			i++
		}
	}
	return argv
}
