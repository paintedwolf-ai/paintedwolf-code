package jq

import (
	"bytes"
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
	dec := json.NewDecoder(strings.NewReader(text))
	var raws []json.RawMessage
	for {
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, err
		}
		raws = append(raws, raw)
	}
	if len(raws) == 0 {
		return nil, errors.New("no JSON value in document")
	}
	edit := &jsonEdit{order: newKeyOrder(), trailing: strings.HasSuffix(text, "\n"), lines: len(raws) > 1}
	for _, raw := range raws {
		vdec := json.NewDecoder(bytes.NewReader(raw))
		vdec.UseNumber()
		v, err := parseJSONValue(vdec, rootPos, 0, edit.order)
		if err != nil {
			return nil, err
		}
		edit.docs = append(edit.docs, v)
		if bytes.ContainsRune(raw, '\n') {
			edit.lines = false
		}
	}
	edit.indent = jsonIndentUnit(raws[0])
	return edit, nil
}

func parseJSONValue(dec *json.Decoder, pos string, depth int, order *keyOrder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
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
			v, err := parseJSONValue(dec, childPos(pos, key), depth+1, order)
			if err != nil {
				return nil, err
			}
			obj[key] = v
		}
		_, err = dec.Token()
		return obj, err
	case '[':
		arr := []any{}
		for dec.More() {
			v, err := parseJSONValue(dec, elemPos(pos), depth+1, order)
			if err != nil {
				return nil, err
			}
			arr = append(arr, v)
		}
		_, err = dec.Token()
		return arr, err
	}
	return nil, fmt.Errorf("unexpected delimiter %q", delim)
}

// jsonIndentUnit reads the indentation of the first nested line.
func jsonIndentUnit(raw []byte) string {
	nl := bytes.IndexByte(raw, '\n')
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
