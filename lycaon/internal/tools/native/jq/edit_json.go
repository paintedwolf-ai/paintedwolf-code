package jq

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/itchyny/gojq"
)

// jsonEdit keeps number literals (json.Number), key order, and layout.
type jsonEdit struct {
	docs  []any
	order *keyOrder
	// indent is the source's indentation unit; empty means compact.
	indent string
	// lines is a stream whose documents each sit on one line (JSON Lines).
	lines    bool
	trailing bool
}

func parseJSONEdit(text string) (*jsonEdit, error) {
	source := strings.TrimSpace(text)
	dec := json.NewDecoder(strings.NewReader(source))
	dec.UseNumber()
	edit := &jsonEdit{order: newKeyOrder(), trailing: strings.HasSuffix(text, "\n")}
	multiline := false
	for {
		start := int(dec.InputOffset())
		token, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		value, err := parseJSONToken(dec, token, rootPos, 0, edit.order)
		if err != nil {
			return nil, err
		}
		raw := strings.TrimSpace(source[start:int(dec.InputOffset())])
		if len(edit.docs) == 0 {
			edit.indent = jsonIndentUnit(raw)
		}
		multiline = multiline || strings.ContainsRune(raw, '\n')
		edit.docs = append(edit.docs, value)
	}
	if len(edit.docs) == 0 {
		return nil, errors.New("no JSON value in document")
	}
	edit.lines = len(edit.docs) > 1 && !multiline
	return edit, nil
}

func parseJSONToken(dec *json.Decoder, tok json.Token, pos string, depth int, order *keyOrder) (any, error) {
	delim, ok := tok.(json.Delim)
	if !ok {
		return tok, nil
	}
	if depth >= maxDocumentDepth {
		return nil, errDocumentDepthExceeded
	}
	switch delim {
	case '{':
		obj := map[string]any{}
		for dec.More() {
			keyTok, err := dec.Token()
			if err != nil {
				return nil, err
			}
			key, _ := keyTok.(string)
			order.note(pos, key)
			token, err := dec.Token()
			if err != nil {
				return nil, err
			}
			v, err := parseJSONToken(dec, token, childPos(pos, key), depth+1, order)
			if err != nil {
				return nil, err
			}
			obj[key] = v
		}
		_, err := dec.Token()
		return obj, err
	case '[':
		arr := []any{}
		for dec.More() {
			token, err := dec.Token()
			if err != nil {
				return nil, err
			}
			v, err := parseJSONToken(dec, token, elemPos(pos), depth+1, order)
			if err != nil {
				return nil, err
			}
			arr = append(arr, v)
		}
		_, err := dec.Token()
		return arr, err
	}
	return nil, fmt.Errorf("unexpected delimiter %q", delim)
}

// jsonIndentUnit reads the indentation of the first nested line.
func jsonIndentUnit(raw string) string {
	nl := strings.IndexByte(raw, '\n')
	if nl < 0 {
		return ""
	}
	line := raw[nl+1:]
	n := 0
	for n < len(line) && (line[n] == ' ' || line[n] == '\t') {
		n++
	}
	if n == 0 {
		return ""
	}
	return string(line[:n])
}

func (d *jsonEdit) inputs() []any { return d.docs }

func (d *jsonEdit) encode(results []editValue) (string, error) {
	indent := d.indent
	if d.lines {
		indent = ""
	}
	var b strings.Builder
	for i, r := range results {
		if i > 0 {
			b.WriteByte('\n')
		}
		if err := d.write(&b, r.value, rootPos, indent, 0); err != nil {
			return "", err
		}
	}
	if d.trailing {
		b.WriteByte('\n')
	}
	return b.String(), nil
}

// write renders jq-flavored JSON: no HTML escaping, number literals verbatim.
func (d *jsonEdit) write(b *strings.Builder, v any, pos, indent string, level int) error {
	switch t := v.(type) {
	case map[string]any:
		if len(t) == 0 {
			b.WriteString("{}")
			return nil
		}
		b.WriteByte('{')
		for i, k := range d.order.ordered(pos, t) {
			if i > 0 {
				b.WriteByte(',')
			}
			d.newline(b, indent, level+1)
			key, _ := gojq.Marshal(k)
			b.Write(key)
			b.WriteByte(':')
			if indent != "" {
				b.WriteByte(' ')
			}
			if err := d.write(b, t[k], childPos(pos, k), indent, level+1); err != nil {
				return err
			}
		}
		d.newline(b, indent, level)
		b.WriteByte('}')
	case []any:
		if len(t) == 0 {
			b.WriteString("[]")
			return nil
		}
		b.WriteByte('[')
		for i, e := range t {
			if i > 0 {
				b.WriteByte(',')
			}
			d.newline(b, indent, level+1)
			if err := d.write(b, e, elemPos(pos), indent, level+1); err != nil {
				return err
			}
		}
		d.newline(b, indent, level)
		b.WriteByte(']')
	default:
		out, err := marshalScalar(t)
		if err != nil {
			return err
		}
		b.Write(out)
	}
	return nil
}

func (d *jsonEdit) newline(b *strings.Builder, indent string, level int) {
	if indent == "" {
		return
	}
	b.WriteByte('\n')
	b.WriteString(strings.Repeat(indent, level))
}

// marshalScalar encodes one query value; gojq panics on foreign types.
func marshalScalar(v any) (out []byte, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = &errEncode{detail: fmt.Sprint(r)}
		}
	}()
	return gojq.Marshal(v)
}
