package jq

import (
	"regexp"
	"sort"
	"strings"

	"github.com/itchyny/gojq"

	"github.com/lycaon/lycaon/internal/runeclamp"
)

const (
	shapeMaxDepth    = 4
	shapeMaxKeys     = 24
	shapeArraySample = 6
	samplePreviewMax = 80
)

// shapeNode summarizes JSON structure for follow-up queries.
type shapeNode struct {
	Type      string       `json:"type"`
	Keys      []shapeField `json:"keys,omitempty"`
	MoreKeys  int          `json:"more_keys,omitempty"`
	Len       int          `json:"len,omitempty"`
	Elem      *shapeNode   `json:"elem,omitempty"`
	Sample    string       `json:"sample,omitempty"`
	ZoomIn    bool         `json:"zoom_in,omitempty"`
	QueryPath string       `json:"query_path,omitempty"`
}

type shapeField struct {
	Key   string     `json:"key"`
	Shape *shapeNode `json:"shape"`
}

var identRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// childPath uses dotted notation when the key permits it.
func childPath(parent, key string) string {
	if identRe.MatchString(key) {
		return parent + "." + key
	}
	return parent + `["` + strings.ReplaceAll(key, `"`, `\"`) + `"]`
}

func normalizePath(path string) string {
	if path == "" {
		return "."
	}
	return path
}

// streamShape summarizes one value directly and many as an array.
func streamShape(results []any) *shapeNode {
	if len(results) == 1 {
		return shapeOf(results[0], 0, "")
	}
	node := &shapeNode{Type: "array", Len: len(results)}
	if len(results) > 0 {
		node.Elem = shapeOf(results[0], 1, "[]")
	}
	node.ZoomIn = true
	return node
}

func shapeOf(v any, depth int, path string) *shapeNode {
	node := &shapeNode{Type: gojq.TypeOf(v), QueryPath: normalizePath(path)}
	switch val := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(val))
		for k := range val {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		if depth >= shapeMaxDepth {
			node.ZoomIn = true
			node.MoreKeys = len(keys)
			return node
		}
		shown := keys
		if len(shown) > shapeMaxKeys {
			node.MoreKeys = len(keys) - shapeMaxKeys
			shown = shown[:shapeMaxKeys]
		}
		for _, k := range shown {
			node.Keys = append(node.Keys, shapeField{
				Key:   k,
				Shape: shapeOf(val[k], depth+1, childPath(path, k)),
			})
		}
	case []any:
		node.Len = len(val)
		if len(val) == 0 {
			return node
		}
		if depth >= shapeMaxDepth {
			node.ZoomIn = true
			return node
		}
		base := path
		if base == "" {
			base = "."
		}
		node.Elem = shapeOf(val[0], depth+1, base+"[]")
		if len(val) > shapeArraySample {
			node.ZoomIn = true
		}
	default:
		node.Sample = runeclamp.Clamp(gojq.Preview(v), samplePreviewMax)
	}
	return node
}
