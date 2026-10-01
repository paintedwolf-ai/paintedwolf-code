package jq

import (
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"regexp"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/pelletier/go-toml/v2/unstable"
)

// tomlEdit re-emits a TOML table in source key order. The source's syntax
// tree decides which tables were inline and which arrays held tables; a
// document whose comments or datetimes the emitter cannot carry is refused.
type tomlEdit struct {
	doc   map[string]any
	order *keyOrder
	// inline marks positions whose table the source wrote inline.
	inline map[string]bool
	// arrayTables marks key paths the source declared with [[ ]].
	arrayTables map[string]bool
}

func parseTOMLEdit(text string) (*tomlEdit, error) {
	edit := &tomlEdit{order: newKeyOrder(), inline: map[string]bool{}, arrayTables: map[string]bool{}}
	if err := edit.readSyntax(text); err != nil {
		return nil, err
	}
	var raw map[string]any
	if _, err := toml.Decode(text, &raw); err != nil {
		return nil, err
	}
	v, err := tomlValue(raw, 0)
	if err != nil {
		return nil, err
	}
	edit.doc, _ = v.(map[string]any)
	return edit, nil
}

// readSyntax walks the syntax tree for what decoded values do not state.
func (d *tomlEdit) readSyntax(text string) error {
	p := unstable.Parser{KeepComments: true}
	p.Reset([]byte(text))
	table := rootPos
	var tablePath []string
	for p.NextExpression() {
		for e := p.Expression(); e != nil; e = e.Next() {
			if err := d.refuseLossy(&p, e); err != nil {
				return err
			}
			switch e.Kind {
			case unstable.Table, unstable.ArrayTable:
				tablePath = tomlKeyPath(e.Key())
				if e.Kind == unstable.ArrayTable {
					d.arrayTables[strings.Join(tablePath, "\x1f")] = true
				}
				table = d.notePath(rootPos, nil, tablePath)
			case unstable.KeyValue:
				keys := tomlKeyPath(e.Key())
				pos := d.notePath(table, tablePath, keys)
				d.noteValue(e.Value(), pos)
			default:
			}
		}
	}
	return p.Error()
}

// notePath records a dotted key under pos and returns its position; array
// tables contribute their element position.
func (d *tomlEdit) notePath(pos string, prefix, keys []string) string {
	full := append(append([]string(nil), prefix...), keys...)
	for i, k := range keys {
		d.order.note(pos, k)
		pos = childPos(pos, k)
		if d.arrayTables[strings.Join(full[:len(prefix)+i+1], "\x1f")] {
			pos = elemPos(pos)
		}
	}
	return pos
}

func (d *tomlEdit) noteValue(n *unstable.Node, pos string) {
	switch n.Kind {
	case unstable.InlineTable:
		d.inline[pos] = true
		for it := n.Children(); it.Next(); {
			kv := it.Node()
			if kv.Kind != unstable.KeyValue {
				continue
			}
			kpos := pos
			for _, k := range tomlKeyPath(kv.Key()) {
				d.order.note(kpos, k)
				kpos = childPos(kpos, k)
			}
			d.noteValue(kv.Value(), kpos)
		}
	case unstable.Array:
		for it := n.Children(); it.Next(); {
			d.noteValue(it.Node(), elemPos(pos))
		}
	default:
	}
}

func (d *tomlEdit) refuseLossy(p *unstable.Parser, n *unstable.Node) error {
	kind := ""
	switch n.Kind {
	case unstable.Comment:
		kind = "comment"
	case unstable.LocalDate, unstable.LocalTime, unstable.LocalDateTime, unstable.DateTime:
		kind = "datetime"
	default:
	}
	if kind != "" {
		line := 0
		if n.Raw.Length > 0 {
			line = p.Shape(n.Raw).Start.Line
		}
		return &errLossy{kind: kind, line: line}
	}
	for it := n.Children(); it.Next(); {
		if err := d.refuseLossy(p, it.Node()); err != nil {
			return err
		}
	}
	return nil
}

func tomlKeyPath(it unstable.Iterator) []string {
	var keys []string
	for it.Next() {
		keys = append(keys, string(it.Node().Data))
	}
	return keys
}

// tomlValue converts decoded TOML to query values; integers stay integers.
func tomlValue(v any, depth int) (any, error) {
	if depth > maxDocumentDepth {
		return nil, errDocumentDepthExceeded
	}
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, e := range t {
			c, err := tomlValue(e, depth+1)
			if err != nil {
				return nil, err
			}
			out[k] = c
		}
		return out, nil
	case []map[string]any:
		out := make([]any, 0, len(t))
		for _, e := range t {
			c, err := tomlValue(e, depth+1)
			if err != nil {
				return nil, err
			}
			out = append(out, c)
		}
		return out, nil
	case []any:
		out := make([]any, 0, len(t))
		for _, e := range t {
			c, err := tomlValue(e, depth+1)
			if err != nil {
				return nil, err
			}
			out = append(out, c)
		}
		return out, nil
	case int64:
		if t >= math.MinInt && t <= math.MaxInt {
			return int(t), nil
		}
		return big.NewInt(t), nil
	case float64, string, bool:
		return t, nil
	}
	return nil, &errLossy{kind: "datetime"}
}

func (d *tomlEdit) inputs() []any { return []any{d.doc} }

func (d *tomlEdit) encode(results []editValue) (string, error) {
	root, ok := results[0].value.(map[string]any)
	if !ok {
		return "", &errEncode{detail: "a TOML document must be a table"}
	}
	var b strings.Builder
	if err := d.table(&b, nil, rootPos, root, false); err != nil {
		return "", err
	}
	return b.String(), nil
}

// table writes a table's own keys, then its sections in source order.
func (d *tomlEdit) table(b *strings.Builder, path []string, pos string, m map[string]any, arrayElem bool) error {
	keys := d.order.ordered(pos, m)
	var plain, sections []string
	for _, k := range keys {
		if d.isSection(path, pos, k, m[k]) {
			sections = append(sections, k)
		} else {
			plain = append(plain, k)
		}
	}
	if len(path) > 0 && (arrayElem || len(plain) > 0 || len(sections) == 0) {
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		open, closing := "[", "]"
		if arrayElem {
			open, closing = "[[", "]]"
		}
		b.WriteString(open + tomlDottedKey(path) + closing + "\n")
	}
	for _, k := range plain {
		b.WriteString(tomlKey(k) + " = ")
		if err := d.inlineValue(b, m[k], childPos(pos, k)); err != nil {
			return err
		}
		b.WriteByte('\n')
	}
	for _, k := range sections {
		sub := append(append([]string(nil), path...), k)
		switch t := m[k].(type) {
		case map[string]any:
			if err := d.table(b, sub, childPos(pos, k), t, false); err != nil {
				return err
			}
		case []any:
			for _, e := range t {
				if err := d.table(b, sub, elemPos(childPos(pos, k)), e.(map[string]any), true); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// isSection reports whether a key is written as its own [table] or [[table]].
func (d *tomlEdit) isSection(path []string, pos, key string, v any) bool {
	cpos := childPos(pos, key)
	switch t := v.(type) {
	case map[string]any:
		return !d.inline[cpos]
	case []any:
		if len(t) == 0 || d.inline[elemPos(cpos)] {
			return false
		}
		for _, e := range t {
			if _, ok := e.(map[string]any); !ok {
				return false
			}
		}
		full := strings.Join(append(append([]string(nil), path...), key), "\x1f")
		_, declared := d.arrayTables[full]
		return declared || !d.order.known(cpos)
	}
	return false
}

func (d *tomlEdit) inlineValue(b *strings.Builder, v any, pos string) error {
	switch t := v.(type) {
	case map[string]any:
		if len(t) == 0 {
			b.WriteString("{}")
			return nil
		}
		b.WriteString("{ ")
		for i, k := range d.order.ordered(pos, t) {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(tomlKey(k) + " = ")
			if err := d.inlineValue(b, t[k], childPos(pos, k)); err != nil {
				return err
			}
		}
		b.WriteString(" }")
	case []any:
		b.WriteByte('[')
		for i, e := range t {
			if i > 0 {
				b.WriteString(", ")
			}
			if err := d.inlineValue(b, e, elemPos(pos)); err != nil {
				return err
			}
		}
		b.WriteByte(']')
	default:
		s, err := tomlScalar(t)
		if err != nil {
			return err
		}
		b.WriteString(s)
	}
	return nil
}

func tomlScalar(v any) (string, error) {
	switch t := v.(type) {
	case nil:
		return "", &errEncode{detail: "TOML has no null value"}
	case bool:
		return strconv.FormatBool(t), nil
	case string:
		return tomlString(t), nil
	case int:
		return strconv.Itoa(t), nil
	case *big.Int:
		if !t.IsInt64() {
			return "", &errEncode{detail: fmt.Sprintf("integer %s exceeds the TOML 64-bit range", t)}
		}
		return t.String(), nil
	case float64:
		return tomlFloat(t), nil
	case json.Number:
		if strings.ContainsAny(t.String(), ".eE") {
			f, err := t.Float64()
			if err != nil {
				return "", &errEncode{detail: err.Error()}
			}
			return tomlFloat(f), nil
		}
		return tomlScalar(bigFromNumber(t))
	}
	return "", &errEncode{detail: fmt.Sprintf("unsupported value %T", v)}
}

func bigFromNumber(n json.Number) any {
	i, ok := new(big.Int).SetString(n.String(), 10)
	if !ok {
		return nil
	}
	return i
}

func tomlFloat(f float64) string {
	switch {
	case math.IsNaN(f):
		return "nan"
	case math.IsInf(f, 1):
		return "inf"
	case math.IsInf(f, -1):
		return "-inf"
	}
	s := strconv.FormatFloat(f, 'g', -1, 64)
	if !strings.ContainsAny(s, ".e") {
		s += ".0"
	}
	return s
}

func tomlString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\b':
			b.WriteString(`\b`)
		case '\t':
			b.WriteString(`\t`)
		case '\n':
			b.WriteString(`\n`)
		case '\f':
			b.WriteString(`\f`)
		case '\r':
			b.WriteString(`\r`)
		default:
			if r < 0x20 || r == 0x7f {
				fmt.Fprintf(&b, `\u%04X`, r)
				continue
			}
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

var tomlBareKey = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

func tomlKey(k string) string {
	if tomlBareKey.MatchString(k) {
		return k
	}
	return tomlString(k)
}

func tomlDottedKey(path []string) string {
	parts := make([]string, len(path))
	for i, k := range path {
		parts[i] = tomlKey(k)
	}
	return strings.Join(parts, ".")
}
