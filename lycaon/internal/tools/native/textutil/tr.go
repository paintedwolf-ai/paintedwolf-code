package textutil

import (
	"bytes"
	"errors"
	"io"
	"strconv"
	"strings"
)

// trClasses are the ASCII character classes; membership never depends on a locale.
var trClasses = map[string]func(byte) bool{
	"alnum":  func(c byte) bool { return isAlpha(c) || isDigit(c) },
	"alpha":  isAlpha,
	"blank":  func(c byte) bool { return c == ' ' || c == '\t' },
	"cntrl":  func(c byte) bool { return c < 0x20 || c == 0x7f },
	"digit":  isDigit,
	"graph":  func(c byte) bool { return c > 0x20 && c < 0x7f },
	"lower":  func(c byte) bool { return c >= 'a' && c <= 'z' },
	"print":  func(c byte) bool { return c >= 0x20 && c < 0x7f },
	"punct":  func(c byte) bool { return c > 0x20 && c < 0x7f && !isAlpha(c) && !isDigit(c) },
	"space":  func(c byte) bool { return c == ' ' || (c >= '\t' && c <= '\r') },
	"upper":  func(c byte) bool { return c >= 'A' && c <= 'Z' },
	"xdigit": func(c byte) bool { return isDigit(c) || (lower(c) >= 'a' && lower(c) <= 'f') },
}

func isAlpha(c byte) bool { return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') }
func isDigit(c byte) bool { return c >= '0' && c <= '9' }

type trSetError string

func (e trSetError) Error() string { return string(e) }

// trSet is one expanded set. fill is the index where SET2's [c*] repeats
// its byte until SET2 is as long as SET1, or -1.
type trSet struct {
	bytes []byte
	fill  int
	with  byte
}

// parseTrSet expands escapes, ranges, [:class:], [=c=] and [c*n] into an
// ordered byte list; [c*] marks a fill. Bytes outside ASCII refuse: their
// meaning differs between byte and character implementations, and this
// alphabet is bytes.
func parseTrSet(text string) (trSet, error) {
	var raw []byte
	var classes, escaped []int
	fill, fillRaw := trSet{fill: -1}, -1
	for i := 0; i < len(text); i++ {
		if strings.HasPrefix(text[i:], "[:") {
			if end := strings.Index(text[i:], ":]"); end > 2 {
				member, ok := trClasses[text[i+2:i+end]]
				if !ok {
					return fill, trSetError("a known [:class:]")
				}
				for c := 0; c < 0x80; c++ {
					if member(byte(c)) {
						raw = append(raw, byte(c))
						classes = append(classes, len(raw)-1)
					}
				}
				i += end + 1
				continue
			}
		}
		if c, count, width, ok := trBracket(text[i:]); ok {
			if count < 0 {
				if fillRaw >= 0 {
					return fill, trSetError("at most one [c*] fill")
				}
				fillRaw, fill.with = len(raw), c
				raw = append(raw, 0)
				classes = append(classes, len(raw)-1)
			}
			for n := 0; n < count; n++ {
				raw = append(raw, c)
				classes = append(classes, len(raw)-1)
			}
			i += width - 1
			continue
		}
		c, width, isEscape := trByte(text[i:])
		if isEscape {
			escaped = append(escaped, len(raw))
		}
		raw = append(raw, c)
		i += width - 1
	}
	for i := 0; i < len(raw); i++ {
		if i == fillRaw {
			fill.fill = len(fill.bytes)
			continue
		}
		if raw[i] >= 0x80 {
			return fill, trSetError("ASCII set members; tr works on bytes")
		}
		if i+2 < len(raw) && raw[i+1] == '-' && !isClassMember(escaped, i+1) && !isClassMember(classes, i) && !isClassMember(classes, i+1) && !isClassMember(classes, i+2) {
			if raw[i+2] < raw[i] {
				return fill, trSetError("ranges in ascending byte order")
			}
			for c := int(raw[i]); c <= int(raw[i+2]); c++ {
				fill.bytes = append(fill.bytes, byte(c))
			}
			i += 2
			continue
		}
		fill.bytes = append(fill.bytes, raw[i])
	}
	return fill, nil
}

// trByte reads one possibly escaped byte and its width in text.
func trByte(text string) (byte, int, bool) {
	if text[0] != '\\' || len(text) == 1 {
		return text[0], 1, false
	}
	e := text[1]
	if b, ok := trEscapes[e]; ok {
		return b, 2, true
	}
	if e >= '0' && e <= '7' {
		value, n := digits(text[1:], 8, 3)
		return byte(value), n + 1, true
	}
	return e, 2, true
}

var trEscapes = map[byte]byte{'n': '\n', 't': '\t', 'r': '\r', 'a': '\a', 'b': '\b', 'f': '\f', 'v': '\v'}

// trBracket reads [=c=] (count 1), [c*n] and [c*] (count -1, a fill).
func trBracket(text string) (c byte, count, width int, ok bool) {
	if len(text) < 4 || text[0] != '[' {
		return 0, 0, 0, false
	}
	if text[1] == '=' {
		c, w, _ := trByte(text[2:])
		if strings.HasPrefix(text[2+w:], "=]") {
			return c, 1, 4 + w, true
		}
		return 0, 0, 0, false
	}
	c, w, _ := trByte(text[1:])
	rest := text[1+w:]
	if !strings.HasPrefix(rest, "*") {
		return 0, 0, 0, false
	}
	end := strings.IndexByte(rest, ']')
	if end < 0 {
		return 0, 0, 0, false
	}
	repeat := rest[1:end]
	if repeat == "" {
		return c, -1, 1 + w + end + 1, true
	}
	base := 10
	if repeat[0] == '0' {
		base = 8
	}
	n, err := strconv.ParseInt(repeat, base, 32)
	if err != nil {
		return 0, 0, 0, false
	}
	if n == 0 {
		return c, -1, 1 + w + end + 1, true
	}
	return c, int(n), 1 + w + end + 1, true
}

func isClassMember(indexes []int, index int) bool {
	for _, i := range indexes {
		if i == index {
			return true
		}
	}
	return false
}

// runTr translates, deletes and squeezes bytes of standard input. -c
// complements SET1 in byte order; a short SET2 is padded with its last byte
// unless -t truncates SET1; -s squeezes the last set given.
func runTr(inv *invocation) error {
	operands := inv.operands()
	del, squeeze := inv.flag("-d"), inv.flag("-s")
	switch {
	case len(operands) == 0:
		return inv.usage(inv.commandWord(), "SET1")
	case len(operands) > 2:
		return inv.usage(operands[2], "at most SET1 and SET2")
	case len(operands) == 1 && !del && !squeeze:
		return inv.usage(operands[0], "SET2 to translate into")
	case len(operands) == 2 && del && !squeeze:
		return inv.usage(operands[1], "no SET2 with -d unless -s")
	}
	sets := make([][]byte, len(operands))
	for i, operand := range operands {
		set, err := parseTrSet(operand.Value)
		if err == nil && set.fill >= 0 && i == 0 {
			err = trSetError("[c*] only in SET2")
		}
		if err != nil {
			return inv.usage(operand, err.Error())
		}
		sets[i] = set.bytes
		if i == 0 && (inv.flag("-c")) {
			sets[0] = complementBytes(set.bytes)
		}
		if set.fill >= 0 {
			repeat := bytes.Repeat([]byte{set.with}, max(len(sets[0])-len(set.bytes), 0))
			sets[i] = append(append(append([]byte{}, set.bytes[:set.fill]...), repeat...), set.bytes[set.fill:]...)
		}
	}
	if len(sets) == 2 && inv.flag("-t") && len(sets[0]) > len(sets[1]) {
		sets[0] = sets[0][:len(sets[1])]
	}
	var deleted, squeezed [256]bool
	var mapping [256]byte
	for c := range mapping {
		mapping[c] = byte(c)
	}
	if len(sets) == 2 && !del {
		if len(sets[1]) == 0 {
			return inv.usage(operands[1], "a nonempty SET2")
		}
		for i, c := range sets[0] {
			mapping[c] = sets[1][min(i, len(sets[1])-1)]
		}
	}
	if del {
		for _, c := range sets[0] {
			deleted[c] = true
		}
	}
	if squeeze {
		for _, c := range sets[len(sets)-1] {
			squeezed[c] = true
		}
	}
	return transformBytes(inv, func(in []byte, out []byte, last *int) []byte {
		for _, c := range in {
			if deleted[c] {
				continue
			}
			c = mapping[c]
			if squeezed[c] && *last == int(c) {
				continue
			}
			*last = int(c)
			out = append(out, c)
		}
		return out
	})
}

// complementBytes is every byte not in set, in ascending order.
func complementBytes(set []byte) []byte {
	var member [256]bool
	for _, c := range set {
		member[c] = true
	}
	var out []byte
	for c := 0; c < 256; c++ {
		if !member[c] {
			out = append(out, byte(c))
		}
	}
	return out
}

// transformBytes streams standard input through a byte transform in bounded chunks.
func transformBytes(inv *invocation, transform func(in, out []byte, last *int) []byte) error {
	const chunk = 32 << 10
	if err := inv.reserve(2 * chunk); err != nil {
		return err
	}
	in, out := make([]byte, chunk), make([]byte, 0, chunk)
	last := -1
	for {
		n, err := inv.streams.Input.Read(in)
		if n > 0 {
			out = transform(in[:n], out[:0], &last)
			if _, writeErr := inv.out.Write(out); writeErr != nil {
				return writeErr
			}
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := inv.ctx.Err(); err != nil {
			return err
		}
	}
}
