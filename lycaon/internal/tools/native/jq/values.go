package jq

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"math/big"
	"slices"
	"strconv"
	"strings"

	"github.com/itchyny/gojq"
	"gopkg.in/yaml.v3"
)

// ValueStream decodes consecutive JSON values from one reader, as jq reads
// its inputs: each complete value is available before the next is parsed.
type ValueStream struct {
	decoder *json.Decoder
}

func NewValueStream(r io.Reader) *ValueStream {
	decoder := json.NewDecoder(r)
	decoder.UseNumber()
	return &ValueStream{decoder: decoder}
}

// Decode returns the next value, io.EOF at the end, or the syntax error.
func (s *ValueStream) Decode() (any, error) {
	var v any
	if err := s.decoder.Decode(&v); err != nil {
		return nil, err
	}
	return normalizeNumbers(v), nil
}

// InputOffset reports how many input bytes the decoded values consumed.
func (s *ValueStream) InputOffset() int64 { return s.decoder.InputOffset() }

func decodeJSONValue(raw []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var v any
	if err := decoder.Decode(&v); err != nil {
		return nil, err
	}
	return normalizeNumbers(v), nil
}

// normalizeNumbers gives JSON numbers gojq's identities: int when exact,
// *big.Int for larger integers, float64 for fractions and exponents.
func normalizeNumbers(v any) any {
	switch v := v.(type) {
	case json.Number:
		return normalizeNumber(v)
	case []any:
		for i, x := range v {
			v[i] = normalizeNumbers(x)
		}
		return v
	case map[string]any:
		for k, x := range v {
			v[k] = normalizeNumbers(x)
		}
		return v
	}
	return v
}

func normalizeNumber(v json.Number) any {
	text := v.String()
	if i, err := v.Int64(); err == nil && math.MinInt <= i && i <= math.MaxInt {
		return int(i)
	}
	if !strings.ContainsAny(text, ".eE") {
		if n, ok := new(big.Int).SetString(text, 10); ok {
			return n
		}
	}
	if f, err := v.Float64(); err == nil {
		return f
	}
	if strings.HasPrefix(text, "-") {
		return -math.MaxFloat64
	}
	return math.MaxFloat64
}

// EncodeJSON is jq's output encoding: gojq's marshaler (sorted keys, jq
// string escapes, NaN as null), compact or indented by two spaces.
func EncodeJSON(v any, indent bool) ([]byte, error) {
	raw, err := gojq.Marshal(v)
	if err != nil || !indent {
		return raw, err
	}
	var out bytes.Buffer
	if err := json.Indent(&out, raw, "", "  "); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// ValueDepth is the nesting depth of a value; scalars are depth zero.
func ValueDepth(v any) int {
	depth := 0
	switch v := v.(type) {
	case []any:
		for _, x := range v {
			depth = max(depth, ValueDepth(x))
		}
		return depth + 1
	case map[string]any:
		for _, x := range v {
			depth = max(depth, ValueDepth(x))
		}
		return depth + 1
	}
	return 0
}

// EncodeYAML renders one value as a YAML document with sorted keys and LF
// line endings. It is a plain serialization, never a format-preserving edit.
func EncodeYAML(v any) ([]byte, error) {
	node, err := yamlNode(v)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	encoder := yaml.NewEncoder(&out)
	encoder.SetIndent(2)
	if err := encoder.Encode(node); err != nil {
		return nil, err
	}
	if err := encoder.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

var errYAMLValue = errors.New("value has no YAML representation")

func yamlNode(v any) (*yaml.Node, error) {
	scalar := func(tag, value string) *yaml.Node { return &yaml.Node{Kind: yaml.ScalarNode, Tag: tag, Value: value} }
	switch v := v.(type) {
	case nil:
		return scalar("!!null", "null"), nil
	case bool:
		return scalar("!!bool", strconv.FormatBool(v)), nil
	case int:
		return scalar("!!int", strconv.Itoa(v)), nil
	case *big.Int:
		return scalar("!!int", v.String()), nil
	case float64:
		raw, err := gojq.Marshal(v)
		if err != nil {
			return nil, err
		}
		return scalar("!!float", string(raw)), nil
	case string:
		return scalar("!!str", v), nil
	case []any:
		node := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		for _, x := range v {
			child, err := yamlNode(x)
			if err != nil {
				return nil, err
			}
			node.Content = append(node.Content, child)
		}
		return node, nil
	case map[string]any:
		node := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		for _, k := range keys {
			child, err := yamlNode(v[k])
			if err != nil {
				return nil, err
			}
			node.Content = append(node.Content, scalar("!!str", k), child)
		}
		return node, nil
	}
	return nil, errYAMLValue
}
