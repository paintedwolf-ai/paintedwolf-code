package jq

import (
	"encoding/json"
	"errors"
	"io"
	"math"
	"math/big"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	yamlNull      = "!!null"
	yamlBool      = "!!bool"
	yamlInt       = "!!int"
	yamlFloat     = "!!float"
	yamlStr       = "!!str"
	yamlTimestamp = "!!timestamp"
	yamlMap       = "!!map"
	yamlSeq       = "!!seq"
	yamlMerge     = "!!merge"
)

// yamlEdit rewrites results onto the source node tree. A subtree whose value
// the query left unchanged is reused whole, so its comments, scalar
// literals, quoting, and key order survive; changed nodes keep the comments
// attached to their position.
type yamlEdit struct {
	docs     []*yaml.Node
	values   []any
	nodeVals map[*yaml.Node]any
	order    *keyOrder
	indent   int
	trailing bool
}

func parseYAMLEdit(text string) (*yamlEdit, error) {
	dec := yaml.NewDecoder(strings.NewReader(text))
	edit := &yamlEdit{nodeVals: map[*yaml.Node]any{}, order: newKeyOrder(), trailing: strings.HasSuffix(text, "\n"), indent: yamlIndent(text)}
	for {
		var doc yaml.Node
		if err := dec.Decode(&doc); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, err
		}
		v, err := edit.value(&doc, rootPos, 0)
		if err != nil {
			return nil, err
		}
		edit.docs = append(edit.docs, &doc)
		edit.values = append(edit.values, v)
	}
	if len(edit.docs) == 0 {
		return nil, errors.New("no document in file")
	}
	return edit, nil
}

// value decodes a node into a query value and records it for reuse checks.
func (d *yamlEdit) value(n *yaml.Node, pos string, depth int) (any, error) {
	if depth > maxDocumentDepth {
		return nil, errDocumentDepthExceeded
	}
	if n.Kind == yaml.AliasNode || n.Anchor != "" {
		return nil, &errLossy{kind: "anchor", line: n.Line}
	}
	var v any
	switch n.Kind {
	case yaml.DocumentNode:
		if len(n.Content) == 0 {
			return nil, nil
		}
		return d.value(n.Content[0], pos, depth)
	case yaml.MappingNode:
		obj := make(map[string]any, len(n.Content)/2)
		for i := 0; i+1 < len(n.Content); i += 2 {
			k := n.Content[i]
			if k.Kind != yaml.ScalarNode {
				return nil, &errLossy{kind: "complex_key", line: k.Line}
			}
			if k.Tag == yamlMerge {
				return nil, &errLossy{kind: "anchor", line: k.Line}
			}
			d.order.note(pos, k.Value)
			cv, err := d.value(n.Content[i+1], childPos(pos, k.Value), depth+1)
			if err != nil {
				return nil, err
			}
			obj[k.Value] = cv
		}
		v = obj
	case yaml.SequenceNode:
		arr := make([]any, 0, len(n.Content))
		for _, c := range n.Content {
			cv, err := d.value(c, elemPos(pos), depth+1)
			if err != nil {
				return nil, err
			}
			arr = append(arr, cv)
		}
		v = arr
	case yaml.ScalarNode:
		sv, err := yamlScalarValue(n)
		if err != nil {
			return nil, err
		}
		v = sv
	default:
	}
	d.nodeVals[n] = v
	return v, nil
}

func yamlScalarValue(n *yaml.Node) (any, error) {
	switch n.ShortTag() {
	case yamlNull:
		return nil, nil
	case yamlBool:
		var b bool
		err := n.Decode(&b)
		return b, err
	case yamlInt:
		i, ok := new(big.Int).SetString(strings.ReplaceAll(n.Value, "_", ""), 0)
		if !ok {
			var f float64
			err := n.Decode(&f)
			return f, err
		}
		if i.IsInt64() && i.Int64() >= math.MinInt && i.Int64() <= math.MaxInt {
			return int(i.Int64()), nil
		}
		return i, nil
	case yamlFloat:
		var f float64
		err := n.Decode(&f)
		return f, err
	}
	return n.Value, nil
}

// yamlIndent is the narrowest indentation the source uses.
func yamlIndent(text string) int {
	best := 0
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimLeft(line, " ")
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if n := len(line) - len(trimmed); n > 0 && (best == 0 || n < best) {
			best = n
		}
	}
	if best < 2 || best > 8 {
		return 2
	}
	return best
}

func (d *yamlEdit) inputs() []any { return d.values }

func (d *yamlEdit) encode(results []editValue) (string, error) {
	var b strings.Builder
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(d.indent)
	for _, r := range results {
		src := d.docs[r.input]
		var content *yaml.Node
		if len(src.Content) > 0 {
			content = src.Content[0]
		}
		merged, err := d.merge(content, r.value, rootPos)
		if err != nil {
			return "", err
		}
		doc := *src
		doc.Content = []*yaml.Node{merged}
		if err := enc.Encode(&doc); err != nil {
			return "", &errEncode{detail: err.Error()}
		}
	}
	if err := enc.Close(); err != nil {
		return "", &errEncode{detail: err.Error()}
	}
	out := b.String()
	if !d.trailing {
		out = strings.TrimSuffix(out, "\n")
	}
	return out, nil
}

// merge renders v at src's position. Entries the result no longer holds leave
// with their comments.
func (d *yamlEdit) merge(src *yaml.Node, v any, pos string) (*yaml.Node, error) {
	if src != nil {
		if sv, ok := d.nodeVals[src]; ok && sameValue(sv, v) {
			return src, nil
		}
	}
	switch t := v.(type) {
	case map[string]any:
		if src != nil && src.Kind == yaml.MappingNode {
			return d.mergeMapping(src, t, pos)
		}
	case []any:
		if src != nil && src.Kind == yaml.SequenceNode {
			return d.mergeSequence(src, t, pos)
		}
	}
	if src != nil && yamlCustomTag(src) {
		return nil, &errLossy{kind: "tag", line: src.Line}
	}
	n, err := d.fresh(v, pos)
	if err != nil {
		return nil, err
	}
	if src != nil {
		n.HeadComment, n.LineComment, n.FootComment = src.HeadComment, src.LineComment, src.FootComment
		if n.Kind == yaml.ScalarNode && src.Kind == yaml.ScalarNode {
			adoptScalarStyle(n, src)
		}
	}
	return n, nil
}

func (d *yamlEdit) mergeMapping(src *yaml.Node, m map[string]any, pos string) (*yaml.Node, error) {
	out := *src
	out.Content = make([]*yaml.Node, 0, 2*len(m))
	placed := make(map[string]any, len(m))
	for i := 0; i+1 < len(src.Content); i += 2 {
		k := src.Content[i]
		nv, ok := m[k.Value]
		if _, done := placed[k.Value]; !ok || done {
			continue
		}
		merged, err := d.merge(src.Content[i+1], nv, childPos(pos, k.Value))
		if err != nil {
			return nil, err
		}
		out.Content = append(out.Content, k, merged)
		placed[k.Value] = nv
	}
	rest := make(map[string]any, len(m)-len(placed))
	for k, nv := range m {
		if _, ok := placed[k]; !ok {
			rest[k] = nv
		}
	}
	for _, k := range d.order.ordered(pos, rest) {
		n, err := d.fresh(rest[k], childPos(pos, k))
		if err != nil {
			return nil, err
		}
		out.Content = append(out.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: yamlStr, Value: k}, n)
	}
	return &out, nil
}

// mergeSequence pairs elements by index when the length holds. Otherwise an
// element is reused only when some later source element equals it; the rest
// are rendered fresh.
func (d *yamlEdit) mergeSequence(src *yaml.Node, arr []any, pos string) (*yaml.Node, error) {
	out := *src
	out.Content = make([]*yaml.Node, 0, len(arr))
	if len(arr) == len(src.Content) {
		for i, e := range arr {
			merged, err := d.merge(src.Content[i], e, elemPos(pos))
			if err != nil {
				return nil, err
			}
			out.Content = append(out.Content, merged)
		}
		return &out, nil
	}
	cursor := 0
	for _, e := range arr {
		var reused *yaml.Node
		for k := cursor; k < len(src.Content); k++ {
			if sv, ok := d.nodeVals[src.Content[k]]; ok && sameValue(sv, e) {
				reused, cursor = src.Content[k], k+1
				break
			}
		}
		if reused == nil {
			n, err := d.fresh(e, elemPos(pos))
			if err != nil {
				return nil, err
			}
			reused = n
		}
		out.Content = append(out.Content, reused)
	}
	return &out, nil
}

// fresh renders a value the source never held.
func (d *yamlEdit) fresh(v any, pos string) (*yaml.Node, error) {
	switch t := v.(type) {
	case map[string]any:
		n := &yaml.Node{Kind: yaml.MappingNode, Tag: yamlMap}
		for _, k := range d.order.ordered(pos, t) {
			c, err := d.fresh(t[k], childPos(pos, k))
			if err != nil {
				return nil, err
			}
			n.Content = append(n.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: yamlStr, Value: k}, c)
		}
		return n, nil
	case []any:
		n := &yaml.Node{Kind: yaml.SequenceNode, Tag: yamlSeq}
		for _, e := range t {
			c, err := d.fresh(e, elemPos(pos))
			if err != nil {
				return nil, err
			}
			n.Content = append(n.Content, c)
		}
		return n, nil
	case nil:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: yamlNull, Value: "null"}, nil
	case bool:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: yamlBool, Value: strconv.FormatBool(t)}, nil
	case int:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: yamlInt, Value: strconv.Itoa(t)}, nil
	case *big.Int:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: yamlInt, Value: t.String()}, nil
	case float64:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: yamlFloat, Value: yamlFloatLiteral(t)}, nil
	case json.Number:
		if strings.ContainsAny(t.String(), ".eE") {
			f, err := t.Float64()
			if err != nil {
				return nil, &errEncode{detail: err.Error()}
			}
			return &yaml.Node{Kind: yaml.ScalarNode, Tag: yamlFloat, Value: yamlFloatLiteral(f)}, nil
		}
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: yamlInt, Value: t.String()}, nil
	case string:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: yamlStr, Value: t}, nil
	}
	return nil, &errEncode{detail: "unsupported value type in result"}
}

// adoptScalarStyle keeps a replaced string's quoting and a timestamp's type.
func adoptScalarStyle(n, src *yaml.Node) {
	if n.Tag != yamlStr {
		return
	}
	switch src.ShortTag() {
	case yamlStr:
		n.Style = src.Style &^ yaml.TaggedStyle
	case yamlTimestamp:
		if (&yaml.Node{Kind: yaml.ScalarNode, Value: n.Value}).ShortTag() == yamlTimestamp {
			n.Tag = yamlTimestamp
		}
	}
}

func yamlFloatLiteral(f float64) string {
	switch {
	case math.IsNaN(f):
		return ".nan"
	case math.IsInf(f, 1):
		return ".inf"
	case math.IsInf(f, -1):
		return "-.inf"
	}
	s := strconv.FormatFloat(f, 'g', -1, 64)
	if !strings.ContainsAny(s, ".e") {
		s += ".0"
	}
	return s
}

// yamlCustomTag reports an application tag, or a core tag whose type a
// query value cannot carry.
func yamlCustomTag(n *yaml.Node) bool {
	switch n.ShortTag() {
	case yamlNull, yamlBool, yamlInt, yamlFloat, yamlStr, yamlTimestamp, yamlMap, yamlSeq:
		return false
	}
	return true
}
