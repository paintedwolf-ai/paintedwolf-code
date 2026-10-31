package gojq

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// Decoder reads consecutive JSON values as jq reads its inputs: objects keep
// their key order (a repeated key keeps its first position and last value),
// and numbers keep their literal in decNumber's canonical scientific form, so
// an unmodified 2.0 prints as 2.0 and 1e3 as 1E+3.
type Decoder struct {
	dec *json.Decoder
}

// NewDecoder reads JSON values from r.
func NewDecoder(r io.Reader) *Decoder {
	dec := json.NewDecoder(r)
	dec.UseNumber()
	return &Decoder{dec: dec}
}

// Decode returns the next complete value, io.EOF after the last one, or
// io.ErrUnexpectedEOF when the input ends inside a value.
func (d *Decoder) Decode() (any, error) {
	tok, err := d.dec.Token()
	if err != nil {
		return nil, err
	}
	v, err := d.value(tok)
	if errors.Is(err, io.EOF) {
		err = io.ErrUnexpectedEOF
	}
	return v, err
}

// InputOffset is the number of input bytes consumed so far.
func (d *Decoder) InputOffset() int64 { return d.dec.InputOffset() }

func (d *Decoder) value(tok json.Token) (any, error) {
	switch tok := tok.(type) {
	case json.Delim:
		switch tok {
		case '{':
			o := NewObject(0)
			for d.dec.More() {
				key, err := d.dec.Token()
				if err != nil {
					return nil, err
				}
				k, ok := key.(string)
				if !ok {
					return nil, fmt.Errorf("object key %v is not a string", key)
				}
				next, err := d.dec.Token()
				if err != nil {
					return nil, err
				}
				v, err := d.value(next)
				if err != nil {
					return nil, err
				}
				o.Set(k, v)
			}
			if _, err := d.dec.Token(); err != nil {
				return nil, err
			}
			return o, nil
		case '[':
			a := []any{}
			for d.dec.More() {
				next, err := d.dec.Token()
				if err != nil {
					return nil, err
				}
				v, err := d.value(next)
				if err != nil {
					return nil, err
				}
				a = append(a, v)
			}
			if _, err := d.dec.Token(); err != nil {
				return nil, err
			}
			return a, nil
		}
		return nil, fmt.Errorf("unexpected delimiter %v", tok)
	case json.Number:
		return CanonicalNumber(tok)
	case string, bool, nil:
		return tok, nil
	}
	return nil, fmt.Errorf("unexpected token %v", tok)
}

var errNumberLiteral = errors.New("invalid number literal")

// CanonicalNumber returns a JSON number literal in decNumber's
// to-scientific-string form, the representation jq prints for a number it
// read and did not change.
func CanonicalNumber(n json.Number) (json.Number, error) {
	s := string(n)
	negative := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	mantissa, exponentText, hasExponent := strings.Cut(strings.ToLower(s), "e")
	exponent := 0
	if hasExponent {
		if _, err := fmt.Sscan(exponentText, &exponent); err != nil {
			return "", errNumberLiteral
		}
	}
	whole, fraction, _ := strings.Cut(mantissa, ".")
	coefficient := strings.TrimLeft(whole+fraction, "0")
	if coefficient == "" {
		coefficient = "0"
	}
	exponent -= len(fraction)
	adjusted := exponent + len(coefficient) - 1
	var out string
	switch {
	case exponent <= 0 && adjusted >= -6:
		if exponent == 0 {
			out = coefficient
		} else if point := len(coefficient) + exponent; point > 0 {
			out = coefficient[:point] + "." + coefficient[point:]
		} else {
			out = "0." + strings.Repeat("0", -point) + coefficient
		}
	default:
		out = coefficient[:1]
		if len(coefficient) > 1 {
			out += "." + coefficient[1:]
		}
		sign := "+"
		if adjusted < 0 {
			sign, adjusted = "-", -adjusted
		}
		out += fmt.Sprintf("E%s%d", sign, adjusted)
	}
	if negative {
		out = "-" + out
	}
	return json.Number(out), nil
}
