package textutil

import (
	"errors"
	"io"
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

// parseTrSet expands escapes, ranges and [:class:] into an ordered byte list.
// Bytes outside ASCII refuse: their meaning differs between byte and
// character implementations, and this alphabet is bytes.
func parseTrSet(text string) ([]byte, error) {
	var raw []byte
	var classes, escaped []int
	for i := 0; i < len(text); i++ {
		if strings.HasPrefix(text[i:], "[:") {
			if end := strings.Index(text[i:], ":]"); end > 2 {
				member, ok := trClasses[text[i+2:i+end]]
				if !ok {
					return nil, trSetError("a known [:class:]")
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
		c := text[i]
		if c == '\\' && i+1 < len(text) {
			escaped = append(escaped, len(raw))
			i++
			switch e := text[i]; e {
			case 'n':
				c = '\n'
			case 't':
				c = '\t'
			case 'r':
				c = '\r'
			case 'a':
				c = '\a'
			case 'b':
				c = '\b'
			case 'f':
				c = '\f'
			case 'v':
				c = '\v'
			default:
				if e >= '0' && e <= '7' {
					value, n := digits(text[i:], 8, 3)
					if value > 0xff {
						return nil, trSetError("an octal escape up to \\377")
					}
					c, i = byte(value), i+n-1
				} else {
					c = e
				}
			}
		}
		raw = append(raw, c)
	}
	var out []byte
	for i := 0; i < len(raw); i++ {
		if raw[i] >= 0x80 {
			return nil, trSetError("ASCII set members; tr works on bytes")
		}
		if i+2 < len(raw) && raw[i+1] == '-' && !isClassMember(escaped, i+1) && !isClassMember(classes, i) && !isClassMember(classes, i+2) {
			if raw[i+2] < raw[i] {
				return nil, trSetError("ranges in ascending byte order")
			}
			for c := int(raw[i]); c <= int(raw[i+2]); c++ {
				out = append(out, byte(c))
			}
			i += 2
			continue
		}
		out = append(out, raw[i])
	}
	return out, nil
}

func isClassMember(indexes []int, index int) bool {
	for _, i := range indexes {
		if i == index {
			return true
		}
	}
	return false
}

// runTr translates, deletes and squeezes bytes of standard input. A short SET2
// is padded with its last byte; -s squeezes the last set given.
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
		if err != nil {
			return inv.usage(operand, err.Error())
		}
		sets[i] = set
	}
	var deleted, squeezed [256]bool
	var mapping [256]byte
	for c := range mapping {
		mapping[c] = byte(c)
	}
	translate := len(sets) == 2 && !del
	if translate {
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
	return inv.transformBytes(func(in []byte, out []byte, last *int) []byte {
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

// transformBytes streams standard input through a byte transform in bounded chunks.
func (inv *invocation) transformBytes(transform func(in, out []byte, last *int) []byte) error {
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
