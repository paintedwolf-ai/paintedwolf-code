package literal

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
)

// odBlock is the bytes per output line.
const odBlock = 16

// odType is one output format: a (named characters), c (escaped
// characters), or d, o, u, x over size-byte little-endian integers.
type odType struct {
	kind  byte
	size  int
	width int
}

// odDigits are the field widths by kind and size.
var odDigits = map[byte]map[int]int{
	'd': {1: 4, 2: 6, 4: 11, 8: 20},
	'o': {1: 3, 2: 6, 4: 11, 8: 22},
	'u': {1: 3, 2: 5, 4: 10, 8: 20},
	'x': {1: 2, 2: 4, 4: 8, 8: 16},
}

// odTraditional are the single-letter format options.
var odTraditional = map[string]string{"-b": "o1", "-c": "c", "-d": "u2", "-o": "o2", "-s": "d2", "-x": "x2"}

// odNames are -t a's names for the ASCII control characters and space.
var odNames = [...]string{"nul", "soh", "stx", "etx", "eot", "enq", "ack", "bel", "bs", "ht", "nl", "vt", "ff", "cr", "so", "si",
	"dle", "dc1", "dc2", "dc3", "dc4", "nak", "syn", "etb", "can", "em", "sub", "esc", "fs", "gs", "rs", "us", "sp"}

// parseOdType reads one -t TYPE word, which may name several formats.
func parseOdType(spec string) ([]odType, bool) {
	var out []odType
	for len(spec) > 0 {
		kind := spec[0]
		spec = spec[1:]
		switch kind {
		case 'a', 'c':
			out = append(out, odType{kind: kind, size: 1, width: 3})
			continue
		case 'd', 'o', 'u', 'x':
		default:
			return nil, false
		}
		size := 4
		switch {
		case spec != "" && strings.IndexByte("CSIL", spec[0]) >= 0:
			size = map[byte]int{'C': 1, 'S': 2, 'I': 4, 'L': 8}[spec[0]]
			spec = spec[1:]
		case spec != "" && spec[0] >= '0' && spec[0] <= '9':
			end := 1
			for end < len(spec) && spec[end] >= '0' && spec[end] <= '9' {
				end++
			}
			n, _ := strconv.Atoi(spec[:end])
			size, spec = n, spec[end:]
		}
		width, ok := odDigits[kind][size]
		if !ok {
			return nil, false
		}
		out = append(out, odType{kind: kind, size: size, width: width})
	}
	return out, len(out) > 0
}

// odTypes collects -t values and traditional letters in argv order.
func odTypes(inv *invocation) ([]odType, error) {
	var out []odType
	for _, item := range inv.argv.Items {
		if item.Option == nil {
			continue
		}
		spec, traditional := odTraditional[item.Option.Key]
		if item.Option.Key == "-t" && len(item.Option.Values) > 0 {
			spec = item.Option.Values[0].Value
		} else if !traditional {
			continue
		}
		types, ok := parseOdType(spec)
		if !ok {
			return nil, inv.usage(spec, "od TYPE a, c, or d, o, u, x with size 1, 2, 4, 8, C, S, I or L")
		}
		out = append(out, types...)
	}
	if len(out) == 0 {
		out, _ = parseOdType("o2")
	}
	return out, nil
}

// odAddress formats offsets for -A; width is its column for later formats.
func odAddress(inv *invocation) (func(int64) string, int, error) {
	radix, _ := inv.value("-A")
	switch radix {
	case "", "o":
		return func(n int64) string { return fmt.Sprintf("%07o", n) }, 7, nil
	case "d":
		return func(n int64) string { return fmt.Sprintf("%07d", n) }, 7, nil
	case "x":
		return func(n int64) string { return fmt.Sprintf("%06x", n) }, 6, nil
	case "n":
		return nil, 0, nil
	}
	return nil, 0, inv.usage(radix, "-A d, o, x or n")
}

// runOd dumps concatenated pinned inputs: 16 bytes per line, one line per
// format, columns aligned across formats, repeated lines collapsed to *
// unless -v.
func runOd(inv *invocation) error {
	types, err := odTypes(inv)
	if err != nil {
		return err
	}
	address, addressWidth, err := odAddress(inv)
	if err != nil {
		return err
	}
	skip, limit := int64(0), int64(math.MaxInt64)
	for key, target := range map[string]*int64{"-j": &skip, "-N": &limit} {
		if value, ok := inv.value(key); ok {
			n, valid := byteCount(value)
			if !valid {
				return inv.usage(value, "byte count for "+key)
			}
			*target = n
		}
	}
	if memory := inv.owners.Revisions.Memory; memory != nil {
		release, err := memory.Reserve(inv.ctx, 64<<10)
		if err != nil {
			return err
		}
		defer release()
	}
	reader := io.Reader(&operandSequence{inv: inv, operands: inv.operandsOrStdin()})
	skipped, err := io.CopyN(io.Discard, reader, skip)
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if skipped < skip {
		inv.fail(1, "", "cannot skip past end of combined input")
		return nil
	}
	reader = io.LimitReader(reader, limit)
	width := 0
	for _, t := range types {
		width = max(width, (t.width+1)*odBlock/t.size)
	}
	verbose := inv.flag("-v")
	var block, previous [odBlock]byte
	hasPrevious, starred := false, false
	offset := skip
	for {
		clear(block[:])
		n, err := io.ReadFull(reader, block[:])
		if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
			return err
		}
		if n == 0 {
			break
		}
		if !verbose && n == odBlock && hasPrevious && bytes.Equal(block[:], previous[:]) {
			if !starred {
				if err := inv.out.text("*\n"); err != nil {
					return err
				}
				starred = true
			}
			offset += int64(n)
			continue
		}
		previous, hasPrevious, starred = block, true, false
		var lines strings.Builder
		for i, t := range types {
			switch {
			case address == nil:
			case i == 0:
				lines.WriteString(address(offset))
			default:
				lines.WriteString(strings.Repeat(" ", addressWidth))
			}
			t.write(&lines, block[:], n, width)
			lines.WriteByte('\n')
		}
		if err := inv.out.text(lines.String()); err != nil {
			return err
		}
		offset += int64(n)
	}
	if address == nil {
		return nil
	}
	return inv.out.text(address(offset) + "\n")
}

// write prints the fields covering n bytes of a zero-filled block. Each
// format's spare width is spread over its fields so every format's line is
// lineWidth columns wide.
func (t odType) write(out *strings.Builder, block []byte, n, lineWidth int) {
	fields := odBlock / t.size
	pad := lineWidth - t.width*fields
	blank := (odBlock - n) / t.size
	remaining := pad
	for i := fields; i > blank; i-- {
		next := pad * (i - 1) / fields
		field := remaining - next + t.width
		remaining = next
		at := (fields - i) * t.size
		fmt.Fprintf(out, "%*s", field, t.render(block[at:at+t.size]))
	}
}

// render formats one datum without its column padding.
func (t odType) render(datum []byte) string {
	var value uint64
	switch t.size {
	case 1:
		value = uint64(datum[0])
	case 2:
		value = uint64(binary.LittleEndian.Uint16(datum))
	case 4:
		value = uint64(binary.LittleEndian.Uint32(datum))
	default:
		value = binary.LittleEndian.Uint64(datum)
	}
	switch t.kind {
	case 'x':
		return fmt.Sprintf("%0*x", t.width, value)
	case 'o':
		return fmt.Sprintf("%0*o", t.width, value)
	case 'u':
		return strconv.FormatUint(value, 10)
	case 'd':
		shift := 64 - 8*t.size
		return strconv.FormatInt(int64(value<<shift)>>shift, 10)
	case 'a':
		c := datum[0] & 0x7f
		switch {
		case c == 0x7f:
			return "del"
		case int(c) < len(odNames):
			return odNames[c]
		}
		return string(rune(c))
	}
	return odChar(datum[0])
}

// odChar is -t c: C escapes, printable ASCII, or three octal digits.
func odChar(c byte) string {
	switch c {
	case 0:
		return `\0`
	case '\a':
		return `\a`
	case '\b':
		return `\b`
	case '\f':
		return `\f`
	case '\n':
		return `\n`
	case '\r':
		return `\r`
	case '\t':
		return `\t`
	case '\v':
		return `\v`
	}
	if c >= 0x20 && c <= 0x7e {
		return string(rune(c))
	}
	return fmt.Sprintf("%03o", c)
}
