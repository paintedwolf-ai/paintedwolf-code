package toolcontract

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"unicode/utf8"
)

// CaptureModelJSON retains canonical model arguments on their program ledger.
func CaptureModelJSON(ctx context.Context, state ReviewStateAdmission, value map[string]any) (string, func(), error) {
	if ctx == nil || state == nil || value == nil {
		return "", nil, fmt.Errorf("model JSON requires context, admission and an object")
	}
	writer := nativeValueWriter{ctx: ctx, state: state}
	defer writer.close()
	if err := writer.jsonValue(value, 0); err != nil {
		return "", nil, err
	}
	retained := &reviewLeases{}
	if err := retained.reserve(ctx, state, int64(len(writer.bytes))); err != nil {
		return "", nil, err
	}
	return string(writer.bytes), retained.release(), nil
}
func (w *nativeValueWriter) jsonValue(value any, depth int) error {
	if depth > nativeValueMaxDepth {
		return fmt.Errorf("model JSON nesting exceeds %d", nativeValueMaxDepth)
	}
	frame, err := w.state.ReserveProgramState(w.ctx, 512)
	if err != nil {
		return err
	}
	if frame == nil {
		return fmt.Errorf("model JSON requires retained admission")
	}
	defer frame()
	switch value := value.(type) {
	case string:
		if !utf8.ValidString(value) {
			return fmt.Errorf("model JSON requires UTF-8 strings")
		}
		return w.quoted(value)
	case map[string]any:
		if value == nil {
			return w.text("null")
		}
		return w.jsonObject(value, depth)
	case []any:
		if value == nil {
			return w.text("null")
		}
		if err := w.text("["); err != nil {
			return err
		}
		for i, child := range value {
			if i > 0 {
				if err := w.text(","); err != nil {
					return err
				}
			}
			if err := w.jsonValue(child, depth+1); err != nil {
				return err
			}
		}
		return w.text("]")
	case []string:
		if value == nil {
			return w.text("null")
		}
		if err := w.text("["); err != nil {
			return err
		}
		for i, child := range value {
			if i > 0 {
				if err := w.text(","); err != nil {
					return err
				}
			}
			if err := w.jsonValue(child, depth+1); err != nil {
				return err
			}
		}
		return w.text("]")
	case nil:
		return w.text("null")
	case bool:
		if value {
			return w.text("true")
		}
		return w.text("false")
	case json.Number, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64:
		size := int64(256)
		if number, ok := value.(json.Number); ok {
			size += 2 * int64(len(number))
		}
		release, err := w.state.ReserveProgramState(w.ctx, size)
		if err != nil {
			return err
		}
		if release == nil {
			return fmt.Errorf("model JSON requires retained admission")
		}
		defer release()
		raw, err := json.Marshal(value)
		if err != nil {
			return err
		}
		return w.text(string(raw))
	default:
		return fmt.Errorf("model arguments require JSON values")
	}
}
func (w *nativeValueWriter) jsonObject(value map[string]any, depth int) error {
	release, err := w.state.ReserveProgramState(w.ctx, 128+int64(len(value))*16)
	if err != nil {
		return err
	}
	if release == nil {
		return fmt.Errorf("model JSON requires retained admission")
	}
	defer release()
	keys := make([]string, 0, len(value))
	for key := range value {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if err := w.text("{"); err != nil {
		return err
	}
	for i, key := range keys {
		if !utf8.ValidString(key) {
			return fmt.Errorf("model JSON requires UTF-8 keys")
		}
		if i > 0 {
			if err := w.text(","); err != nil {
				return err
			}
		}
		if err := w.quoted(key); err != nil {
			return err
		}
		if err := w.text(":"); err != nil {
			return err
		}
		if err := w.jsonValue(value[key], depth+1); err != nil {
			return err
		}
	}
	return w.text("}")
}
