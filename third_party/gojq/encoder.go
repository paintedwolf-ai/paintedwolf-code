package gojq

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/big"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// Marshal returns the jq-flavored JSON encoding of v.
//
// This method accepts only limited types (nil, bool, int, float64, *big.Int,
// json.Number, string, []any, and *OrderedObject) because these are the
// possible types a gojq iterator can emit. This method marshals NaN to null,
// truncates infinities to (+|-) math.MaxFloat64, uses \b and \f in strings,
// and does not escape '<', '>', '&', '\u2028', and '\u2029'. These behaviors
// are based on the marshaler of jq command, and different from json.Marshal in
// the Go standard library. Note that the result is not safe to embed in HTML.
func Marshal(v any) ([]byte, error) {
	var b bytes.Buffer
	(&encoder{w: &b}).encode(v)
	return b.Bytes(), nil
}

// MarshalOptions selects jq's output layout flags.
type MarshalOptions struct {
	// Pretty puts each array element and object member on its own line,
	// nested by Indent per level; otherwise output is compact.
	Pretty bool
	Indent string
	// SortKeys prints object keys in codepoint order (--sort-keys).
	SortKeys bool
	// ASCII escapes every non-ASCII character as \uXXXX (--ascii-output).
	ASCII bool
}

// MarshalWith is Marshal with jq's layout flags.
func MarshalWith(v any, options MarshalOptions) ([]byte, error) {
	var b bytes.Buffer
	(&encoder{w: &b, sortKeys: options.SortKeys, ascii: options.ASCII, pretty: options.Pretty, indent: options.Indent}).encode(v)
	return b.Bytes(), nil
}

func jsonMarshal(v any) string {
	var sb strings.Builder
	(&encoder{w: &sb}).encode(v)
	return sb.String()
}

func jsonEncodeString(sb *strings.Builder, v string) {
	(&encoder{w: sb}).encodeString(v)
}

type encoder struct {
	w interface {
		io.Writer
		io.ByteWriter
		io.StringWriter
	}
	buf      [64]byte
	sortKeys bool
	ascii    bool
	pretty   bool
	indent   string
	depth    int
}

// newline starts a line at the current depth in indented output.
func (e *encoder) newline() {
	if !e.pretty {
		return
	}
	e.w.WriteByte('\n')
	for range e.depth {
		e.w.WriteString(e.indent)
	}
}

func (e *encoder) encode(v any) {
	switch v := v.(type) {
	case nil:
		e.w.WriteString("null")
	case bool:
		if v {
			e.w.WriteString("true")
		} else {
			e.w.WriteString("false")
		}
	case int:
		if v > maxExactInt || v < -maxExactInt {
			e.encodeFloat64(float64(v))
			return
		}
		e.w.Write(strconv.AppendInt(e.buf[:0], int64(v), 10))
	case float64:
		e.encodeFloat64(v)
	case *big.Int:
		e.encodeFloat64(bigToFloat(v))
	case json.Number:
		e.w.WriteString(v.String())
	case string:
		e.encodeString(v)
	case []any:
		e.encodeArray(v)
	case *OrderedObject:
		e.encodeObject(v)
	default:
		panic(fmt.Sprintf("invalid type: %[1]T (%[1]v)", v))
	}
}

// encodeFloat64 prints a double as jq does (jvp_dtoa_fmt): the shortest
// digits that round-trip, in exponent form when the decimal point falls four
// or more places left of the digits or more than fifteen places past them.
func (e *encoder) encodeFloat64(f float64) {
	if math.IsNaN(f) {
		e.w.WriteString("null")
		return
	}
	f = min(max(f, -math.MaxFloat64), math.MaxFloat64)
	if f == 0 {
		if math.Signbit(f) {
			e.w.WriteString("-0")
		} else {
			e.w.WriteString("0")
		}
		return
	}
	sci := strconv.AppendFloat(e.buf[:0], f, 'e', -1, 64)
	negative := sci[0] == '-'
	if negative {
		sci = sci[1:]
	}
	mantissa, exponent, _ := strings.Cut(string(sci), "e")
	digits := strings.Replace(mantissa, ".", "", 1)
	exp, _ := strconv.Atoi(exponent)
	point := exp + 1
	var out strings.Builder
	if negative {
		out.WriteByte('-')
	}
	switch {
	case point <= -4 || point > len(digits)+15:
		out.WriteString(digits[:1])
		if len(digits) > 1 {
			out.WriteString("." + digits[1:])
		}
		sign := "+"
		if exp < 0 {
			sign, exp = "-", -exp
		}
		out.WriteString(fmt.Sprintf("e%s%02d", sign, exp))
	case point <= 0:
		out.WriteString("0." + strings.Repeat("0", -point) + digits)
	case point >= len(digits):
		out.WriteString(digits + strings.Repeat("0", point-len(digits)))
	default:
		out.WriteString(digits[:point] + "." + digits[point:])
	}
	e.w.WriteString(out.String())
}

// ref: encodeState#string in encoding/json
func (e *encoder) encodeString(s string) {
	e.w.WriteByte('"')
	start := 0
	for i := 0; i < len(s); {
		if b := s[i]; b < utf8.RuneSelf {
			if ' ' <= b && b <= '~' && b != '"' && b != '\\' {
				i++
				continue
			}
			if start < i {
				e.w.WriteString(s[start:i])
			}
			switch b {
			case '"':
				e.w.WriteString(`\"`)
			case '\\':
				e.w.WriteString(`\\`)
			case '\b':
				e.w.WriteString(`\b`)
			case '\f':
				e.w.WriteString(`\f`)
			case '\n':
				e.w.WriteString(`\n`)
			case '\r':
				e.w.WriteString(`\r`)
			case '\t':
				e.w.WriteString(`\t`)
			default:
				const hex = "0123456789abcdef"
				e.w.WriteString(`\u00`)
				e.w.WriteByte(hex[b>>4])
				e.w.WriteByte(hex[b&0xF])
			}
			i++
			start = i
			continue
		}
		c, size := utf8.DecodeRuneInString(s[i:])
		if c == utf8.RuneError && size == 1 {
			if start < i {
				e.w.WriteString(s[start:i])
			}
			e.w.WriteString(`\ufffd`)
			i += size
			start = i
			continue
		}
		if e.ascii {
			if start < i {
				e.w.WriteString(s[start:i])
			}
			if c > 0xFFFF {
				r1, r2 := utf16.EncodeRune(c)
				e.w.WriteString(fmt.Sprintf(`\u%04x\u%04x`, r1, r2))
			} else {
				e.w.WriteString(fmt.Sprintf(`\u%04x`, c))
			}
			i += size
			start = i
			continue
		}
		i += size
	}
	if start < len(s) {
		e.w.WriteString(s[start:])
	}
	e.w.WriteByte('"')
}

func (e *encoder) encodeArray(vs []any) {
	e.w.WriteByte('[')
	if len(vs) == 0 {
		e.w.WriteByte(']')
		return
	}
	e.depth++
	for i, v := range vs {
		if i > 0 {
			e.w.WriteByte(',')
		}
		e.newline()
		e.encode(v)
	}
	e.depth--
	e.newline()
	e.w.WriteByte(']')
}

func (e *encoder) encodeObject(vs *OrderedObject) {
	e.w.WriteByte('{')
	keys := vs.Keys()
	if e.sortKeys {
		keys = vs.SortedKeys()
	}
	if len(keys) == 0 {
		e.w.WriteByte('}')
		return
	}
	e.depth++
	for i, k := range keys {
		if i > 0 {
			e.w.WriteByte(',')
		}
		e.newline()
		e.encodeString(k)
		e.w.WriteByte(':')
		if e.pretty {
			e.w.WriteByte(' ')
		}
		v, _ := vs.Get(k)
		e.encode(v)
	}
	e.depth--
	e.newline()
	e.w.WriteByte('}')
}
