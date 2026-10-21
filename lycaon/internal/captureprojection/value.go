package captureprojection

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
)

// JSON screens strings and object keys while preserving JSON structure.
func (p *Projector) JSON(ctx context.Context, scope Scope, label string, raw []byte) ([]byte, Metadata, error) {
	if p == nil || p.matcher == nil {
		return nil, Metadata{}, ErrUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if len(raw) == 0 {
		return nil, Metadata{StructuredCoverage: true}, nil
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, Metadata{}, fmt.Errorf("decode captured JSON: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		if err == nil {
			return nil, Metadata{}, fmt.Errorf("decode captured JSON: multiple values")
		}
		return nil, Metadata{}, fmt.Errorf("decode captured JSON trailer: %w", err)
	}
	meta := Metadata{StructuredCoverage: true}
	walker := valueWalk{screen: func(label, value string) (string, error) {
		projected, err := p.Text(ctx, scope, label, value)
		mergeMetadata(&meta, projected.Metadata)
		return projected.Value, err
	}}
	projected, err := walker.walk(ctx, label, value, 0)
	if err != nil {
		return nil, Metadata{}, err
	}
	out, err := json.Marshal(projected)
	if err != nil {
		return nil, Metadata{}, fmt.Errorf("encode screened capture JSON: %w", err)
	}
	return out, meta, nil
}

// valueWalk is shared by legacy JSON capture and admitted native value capture.
// The latter supplies accounting before containers, keys, labels or values grow.
type valueWalk struct {
	screen    func(string, string) (string, error)
	admit     func(context.Context, int64) error
	node      func(context.Context, int) error
	scalar    func(string) (string, error)
	generated func(int64) error
}

func (w *valueWalk) reserve(ctx context.Context, n int64) error {
	if w.admit != nil {
		return w.admit(ctx, n)
	}
	return nil
}

func (w *valueWalk) walk(ctx context.Context, label string, value any, depth int) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if w.node != nil {
		if err := w.node(ctx, depth); err != nil {
			return nil, err
		}
	}
	switch typed := value.(type) {
	case string:
		return w.screen(label, typed)
	case []any:
		if err := w.reserve(ctx, int64(len(typed))*32+4096); err != nil {
			return nil, err
		}
		out := make([]any, len(typed))
		for i := range typed {
			child, err := w.walk(ctx, label, typed[i], depth+1)
			if err != nil {
				return nil, err
			}
			out[i] = child
		}
		return out, nil
	case map[string]any:
		if err := w.reserve(ctx, int64(len(typed))*256+4096); err != nil {
			return nil, err
		}
		out := make(map[string]any, len(typed))
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		if err := w.reserve(ctx, int64(len(label))+4100); err != nil {
			return nil, err
		}
		keyLabel := label + ".key"
		for _, key := range keys {
			if w.node != nil {
				if err := w.node(ctx, depth+1); err != nil {
					return nil, err
				}
			}
			safeKey, err := w.screen(keyLabel, key)
			if err != nil {
				return nil, err
			}
			safeKey, err = w.uniqueKey(ctx, out, safeKey)
			if err != nil {
				return nil, err
			}
			if err := w.reserve(ctx, int64(len(label))+int64(len(safeKey))+4097); err != nil {
				return nil, err
			}
			child, err := w.walk(ctx, label+"."+safeKey, typed[key], depth+1)
			if err != nil {
				return nil, err
			}
			out[safeKey] = child
		}
		return out, nil
	case json.Number:
		if w.scalar != nil {
			copied, err := w.scalar(string(typed))
			return json.Number(copied), err
		}
		return typed, nil
	case nil, bool:
		return value, nil
	default:
		return nil, fmt.Errorf("unsupported captured value type %T", value)
	}
}

func (w *valueWalk) uniqueKey(ctx context.Context, values map[string]any, key string) (string, error) {
	if _, exists := values[key]; !exists {
		return key, nil
	}
	for i := 2; ; i++ {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if err := w.reserve(ctx, int64(len(key))+4096+32); err != nil {
			return "", err
		}
		candidate := fmt.Sprintf("%s#%d", key, i)
		if _, exists := values[candidate]; !exists {
			if w.generated != nil {
				if err := w.generated(int64(len(candidate) - len(key))); err != nil {
					return "", err
				}
			}
			return candidate, nil
		}
	}
}
