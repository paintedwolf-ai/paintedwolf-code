package argdiag

import (
	"encoding/json"
	"strconv"
)

// DefectKind names where text stops being JSON.
type DefectKind string

const (
	// DefectUnclosed: the text ends while objects or arrays are still open.
	DefectUnclosed DefectKind = "unclosed"
	// DefectUnexpectedToken: a token the grammar does not allow at that point.
	DefectUnexpectedToken DefectKind = "unexpected_token"
	// DefectUnterminatedString: a string opened and never closed.
	DefectUnterminatedString DefectKind = "unterminated_string"
	// DefectTrailingText: text follows one complete value.
	DefectTrailingText DefectKind = "trailing_text"
)

// Defect locates the first point a text departs from the JSON grammar.
type Defect struct {
	Kind DefectKind
	// Offset is the byte offset of the defect within the text.
	Offset int
	// OpenPaths are the containers still open at the defect, outermost first,
	// as paths relative to the text's own value ("" is the value itself).
	OpenPaths []string
}

// lenientJSON is what a reader recovers from text that is not valid JSON.
type lenientJSON struct {
	// Value is the structure read before the defect, with open containers closed.
	Value any
	// KeyOrder lists each object's members in document order by relative path.
	KeyOrder map[string][]string
	Defect   Defect
	// Trailing holds members written after a complete object value, in
	// document order: the text closed the object before them.
	Trailing      map[string]any
	TrailingOrder []string
}

// readLenientJSON reads text that failed strict decoding. The structure it
// returns is the text as written, which is what explains the defect: a member
// that lands inside the wrong container shows up there.
func readLenientJSON(text string) lenientJSON {
	r := &lenientReader{text: text, order: map[string][]string{}}
	value, ok := r.value("")
	var trailing map[string]any
	var trailingOrder []string
	if ok {
		r.skipSpace()
		if r.pos < len(r.text) && r.defect == nil {
			r.fail(DefectTrailingText, r.pos)
			if _, isObject := value.(map[string]any); isObject && r.text[r.pos] == ',' {
				trailing, trailingOrder = r.trailingMembers()
			}
		}
	}
	out := lenientJSON{Value: value, KeyOrder: r.order, Trailing: trailing, TrailingOrder: trailingOrder}
	if r.defect != nil {
		out.Defect = *r.defect
	}
	return out
}

type lenientReader struct {
	text   string
	pos    int
	open   []string
	order  map[string][]string
	defect *Defect
}

// trailingMembers reads `, "key": value` pairs after a closed object, up to
// a closing brace or the first token that is not one.
func (r *lenientReader) trailingMembers() (map[string]any, []string) {
	members := map[string]any{}
	var order []string
	for r.pos < len(r.text) && r.text[r.pos] == ',' {
		r.pos++
		if r.atEnd() || r.text[r.pos] != '"' {
			break
		}
		raw, ok := r.str()
		key, _ := raw.(string)
		if !ok || r.atEnd() || r.text[r.pos] != ':' {
			break
		}
		r.pos++
		value, ok := r.value(key)
		if !ok {
			break
		}
		members[key] = value
		order = append(order, key)
		r.skipSpace()
	}
	return members, order
}

func (r *lenientReader) fail(kind DefectKind, offset int) {
	if r.defect != nil {
		return
	}
	r.defect = &Defect{Kind: kind, Offset: offset, OpenPaths: append([]string(nil), r.open...)}
}

func (r *lenientReader) skipSpace() {
	for r.pos < len(r.text) {
		switch r.text[r.pos] {
		case ' ', '\t', '\n', '\r':
			r.pos++
		default:
			return
		}
	}
}

func (r *lenientReader) atEnd() bool {
	r.skipSpace()
	return r.pos >= len(r.text)
}

// value reads one value at path. ok is false once a defect stops reading.
func (r *lenientReader) value(path string) (any, bool) {
	if r.atEnd() {
		r.fail(DefectUnclosed, r.pos)
		return nil, false
	}
	switch r.text[r.pos] {
	case '{':
		return r.object(path)
	case '[':
		return r.array(path)
	case '"':
		return r.str()
	default:
		return r.scalar()
	}
}

func (r *lenientReader) object(path string) (any, bool) {
	out := map[string]any{}
	r.open = append(r.open, path)
	r.pos++
	for first := true; ; first = false {
		if r.atEnd() {
			r.fail(DefectUnclosed, r.pos)
			return out, false
		}
		if r.text[r.pos] == '}' && first {
			r.pos++
			r.open = r.open[:len(r.open)-1]
			return out, true
		}
		if r.text[r.pos] != '"' {
			r.fail(DefectUnexpectedToken, r.pos)
			return out, false
		}
		raw, ok := r.str()
		if !ok {
			return out, false
		}
		key, _ := raw.(string)
		if r.atEnd() {
			r.fail(DefectUnclosed, r.pos)
			return out, false
		}
		if r.text[r.pos] != ':' {
			r.fail(DefectUnexpectedToken, r.pos)
			return out, false
		}
		r.pos++
		child := joinArgPath(path, key)
		value, ok := r.value(child)
		if value != nil || ok {
			out[key] = value
			r.order[path] = append(r.order[path], key)
		}
		if !ok {
			return out, false
		}
		if r.atEnd() {
			r.fail(DefectUnclosed, r.pos)
			return out, false
		}
		switch r.text[r.pos] {
		case ',':
			r.pos++
		case '}':
			r.pos++
			r.open = r.open[:len(r.open)-1]
			return out, true
		default:
			r.fail(DefectUnexpectedToken, r.pos)
			return out, false
		}
	}
}

func (r *lenientReader) array(path string) (any, bool) {
	out := []any{}
	r.open = append(r.open, path)
	r.pos++
	for i := 0; ; i++ {
		if r.atEnd() {
			r.fail(DefectUnclosed, r.pos)
			return out, false
		}
		if r.text[r.pos] == ']' && i == 0 {
			r.pos++
			r.open = r.open[:len(r.open)-1]
			return out, true
		}
		value, ok := r.value(path + "[" + strconv.Itoa(i) + "]")
		if value != nil || ok {
			out = append(out, value)
		}
		if !ok {
			return out, false
		}
		if r.atEnd() {
			r.fail(DefectUnclosed, r.pos)
			return out, false
		}
		switch r.text[r.pos] {
		case ',':
			r.pos++
		case ']':
			r.pos++
			r.open = r.open[:len(r.open)-1]
			return out, true
		default:
			r.fail(DefectUnexpectedToken, r.pos)
			return out, false
		}
	}
}

func (r *lenientReader) str() (any, bool) {
	start := r.pos
	for i := r.pos + 1; i < len(r.text); i++ {
		switch r.text[i] {
		case '\\':
			i++
		case '"':
			var s string
			if err := json.Unmarshal([]byte(r.text[start:i+1]), &s); err != nil {
				r.fail(DefectUnexpectedToken, start)
				return nil, false
			}
			r.pos = i + 1
			return s, true
		}
	}
	r.fail(DefectUnterminatedString, start)
	return nil, false
}

func (r *lenientReader) scalar() (any, bool) {
	start := r.pos
	for r.pos < len(r.text) && isScalarByte(r.text[r.pos]) {
		r.pos++
	}
	var v any
	if start == r.pos || json.Unmarshal([]byte(r.text[start:r.pos]), &v) != nil {
		r.pos = start
		r.fail(DefectUnexpectedToken, start)
		return nil, false
	}
	return v, true
}

func isScalarByte(b byte) bool {
	return b == '-' || b == '+' || b == '.' || (b >= '0' && b <= '9') || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// joinArgPath joins a dotted argument path; "" is the root.
func joinArgPath(base, key string) string {
	if base == "" {
		return key
	}
	return base + "." + key
}
