package textutil

import (
	"fmt"
	"github.com/lycaon/lycaon/internal/tools"
	"io"
	"math"
	"strconv"
	"strings"
)

// pad emits arbitrary text widths in bounded chunks and stops on the first
// downstream error. Width never becomes an allocation size.
func pad(out io.Writer, d directive, text string) error {
	if d.precision != "" {
		if limit, _ := strconv.Atoi(d.precision[1:]); limit < len(text) {
			text = text[:limit]
		}
	}
	width, _ := strconv.Atoi(d.width)
	remaining := max(width-len(text), 0)
	left := strings.Contains(d.flags, "-")
	if left {
		if _, err := io.WriteString(out, text); err != nil {
			return err
		}
	}
	if err := writePadding(out, ' ', remaining); err != nil {
		return err
	}
	if !left {
		_, err := io.WriteString(out, text)
		return err
	}
	return nil
}

func writePadding(out io.Writer, fill byte, remaining int) error {
	if remaining <= 0 {
		return nil
	}
	chunk := strings.Repeat(string(fill), min(remaining, 32<<10))
	for remaining > 0 {
		n := min(remaining, len(chunk))
		if _, err := io.WriteString(out, chunk[:n]); err != nil {
			return err
		}
		remaining -= n
	}
	return nil
}

// Numeric precision needs bounded conversion storage. Width is only padding:
// keep it out of fmt's allocation and emit it after the core conversion.
func writeNumber(inv *invocation, out io.Writer, d directive, verb string, value any) error {
	maximum := inv.limit("record_bytes")
	var precision uint64
	if digits := strings.TrimPrefix(d.precision, "."); digits != "" {
		n, err := strconv.ParseUint(digits, 10, 63)
		if err != nil || n > uint64(maximum) {
			observed := int64(min(n, 1<<62))
			if err != nil {
				observed = 1 << 62
			}
			return tools.CommandLimitExceeded("record_bytes", maximum, observed)
		}
		precision = n
	}
	release, err := inv.ports.Memory.Reserve(inv.ctx, 4*(int64(precision)+1024))
	if err != nil {
		return err
	}
	defer release()
	text := fmt.Sprintf("%"+d.flags+d.precision+verb, value)
	if int64(len(text)) > maximum {
		return tools.CommandLimitExceeded("record_bytes", maximum, int64(len(text)))
	}
	zero := strings.Contains(d.flags, "0") && !strings.Contains(d.flags, "-")
	if strings.ContainsRune("diouxX", rune(d.verb)) && d.precision != "" {
		zero = false
	}
	if number, ok := value.(float64); ok && (math.IsInf(number, 0) || math.IsNaN(number)) {
		zero = false
	}
	if !zero {
		d.precision = ""
		return pad(out, d, text)
	}
	width, _ := strconv.Atoi(d.width)
	prefix := 0
	if len(text) > 0 && strings.ContainsRune("+- ", rune(text[0])) {
		prefix = 1
	}
	if strings.HasPrefix(text[prefix:], "0x") || strings.HasPrefix(text[prefix:], "0X") {
		prefix += 2
	}
	if _, err := io.WriteString(out, text[:prefix]); err != nil {
		return err
	}
	if err := writePadding(out, '0', max(width-len(text), 0)); err != nil {
		return err
	}
	_, err = io.WriteString(out, text[prefix:])
	return err
}
