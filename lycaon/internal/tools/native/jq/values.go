package jq

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"strconv"
	"strings"

	"github.com/itchyny/gojq"
	"gopkg.in/yaml.v3"
)

// ValueStream decodes consecutive JSON values from one reader, as jq reads
// its inputs: each complete value is available before the next is parsed,
// objects keep their key order and numbers keep their literal.
type ValueStream struct {
	decoder *gojq.Decoder
}

func NewValueStream(r io.Reader) *ValueStream {
	return &ValueStream{decoder: gojq.NewDecoder(r)}
}

// Decode returns the next value, io.EOF at the end, or the syntax error.
func (s *ValueStream) Decode() (any, error) { return s.decoder.Decode() }

// InputOffset reports how many input bytes the decoded values consumed.
func (s *ValueStream) InputOffset() int64 { return s.decoder.InputOffset() }

func decodeJSONValue(raw []byte) (any, error) {
	return gojq.NewDecoder(bytes.NewReader(raw)).Decode()
}

// EncodeJSON is jq's default output encoding: insertion-ordered keys, jq
// string escapes and number literals, compact or indented by two spaces.
func EncodeJSON(v any, indent bool) ([]byte, error) {
	return gojq.MarshalWith(v, gojq.MarshalOptions{Pretty: indent, Indent: "  "})
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
	case *gojq.OrderedObject:
		v.Range(func(_ string, x any) bool {
			depth = max(depth, ValueDepth(x))
			return true
		})
		return depth + 1
	}
	return 0
}

// EncodeYAML renders one value as a YAML document in key order with LF
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
	case json.Number:
		if strings.ContainsAny(string(v), ".eE") {
			return scalar("!!float", string(v)), nil
		}
		return scalar("!!int", string(v)), nil
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
	case *gojq.OrderedObject:
		node := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		for _, k := range v.Keys() {
			x, _ := v.Get(k)
			child, err := yamlNode(x)
			if err != nil {
				return nil, err
			}
			node.Content = append(node.Content, scalar("!!str", k), child)
		}
		return node, nil
	}
	return nil, errYAMLValue
}
