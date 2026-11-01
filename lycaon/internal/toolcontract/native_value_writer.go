package toolcontract

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"unicode/utf8"
)

type nativeValueWriter struct {
	ctx     context.Context
	state   ReviewStateAdmission
	bytes   []byte
	release func()
}

func (w *nativeValueWriter) close() {
	if w.release != nil {
		w.release()
		w.release = nil
	}
	w.bytes = nil
}
func (w *nativeValueWriter) Write(p []byte) (int, error) {
	if err := w.text(string(p)); err != nil {
		return 0, err
	}
	return len(p), nil
}
func (w *nativeValueWriter) text(s string) error {
	if err := w.ctx.Err(); err != nil {
		return err
	}
	if len(s) > int(^uint(0)>>1)-len(w.bytes) {
		return fmt.Errorf("native value size overflow")
	}
	needed := len(w.bytes) + len(s)
	if needed > cap(w.bytes) {
		capacity := max(256, max(needed, 2*cap(w.bytes)))
		release, err := w.state.ReserveProgramState(w.ctx, int64(capacity)+128)
		if err != nil {
			return err
		}
		if release == nil {
			return fmt.Errorf("native encoding requires retained admission")
		}
		next := make([]byte, len(w.bytes), capacity)
		copy(next, w.bytes)
		if w.release != nil {
			w.release()
		}
		w.bytes, w.release = next, release
	}
	w.bytes = append(w.bytes, s...)
	return nil
}
func (w *nativeValueWriter) value(v any, depth int) error {
	if depth > nativeValueMaxDepth {
		return fmt.Errorf("native value nesting exceeds %d", nativeValueMaxDepth)
	}
	frame, err := w.state.ReserveProgramState(w.ctx, 512)
	if err != nil {
		return err
	}
	if frame == nil {
		return fmt.Errorf("native encoding requires retained admission")
	}
	defer frame()
	switch v := v.(type) {
	case nil:
		return w.text(`["null"]`)
	case bool:
		if v {
			return w.text(`["bool",true]`)
		}
		return w.text(`["bool",false]`)
	case string:
		return w.stringValue(v)
	case map[string]any:
		return w.object(v, depth)
	case []any:
		if err := w.text(`["array",[`); err != nil {
			return err
		}
		for i, item := range v {
			if i > 0 {
				if err := w.text(","); err != nil {
					return err
				}
			}
			if err := w.value(item, depth+1); err != nil {
				return err
			}
		}
		return w.text("]]")
	case []string:
		if err := w.text(`["array",[`); err != nil {
			return err
		}
		for i, item := range v {
			if i > 0 {
				if err := w.text(","); err != nil {
					return err
				}
			}
			if err := w.value(item, depth+1); err != nil {
				return err
			}
		}
		return w.text("]]")
	case json.Number, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64:
		size := int64(256)
		if n, ok := v.(json.Number); ok {
			size += 2 * int64(len(n))
		}
		release, err := w.state.ReserveProgramState(w.ctx, size)
		if err != nil {
			return err
		}
		if release == nil {
			return fmt.Errorf("native encoding requires retained admission")
		}
		defer release()
		raw, err := json.Marshal(v)
		if err != nil {
			return err
		}
		if err := w.text(`["number",`); err != nil {
			return err
		}
		if err := w.quoted(string(raw)); err != nil {
			return err
		}
		return w.text("]")
	default:
		return fmt.Errorf("review arguments require JSON scalar/container values")
	}
}
func (w *nativeValueWriter) object(v map[string]any, depth int) error {
	release, err := w.state.ReserveProgramState(w.ctx, 128+int64(len(v))*16)
	if err != nil {
		return err
	}
	if release == nil {
		return fmt.Errorf("native encoding requires retained admission")
	}
	defer release()
	keys := make([]string, 0, len(v))
	for key := range v {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if err := w.text(`["object",[`); err != nil {
		return err
	}
	for i, key := range keys {
		if i > 0 {
			if err := w.text(","); err != nil {
				return err
			}
		}
		if err := w.text("["); err != nil {
			return err
		}
		if err := w.stringValue(key); err != nil {
			return err
		}
		if err := w.text(","); err != nil {
			return err
		}
		if err := w.value(v[key], depth+1); err != nil {
			return err
		}
		if err := w.text("]"); err != nil {
			return err
		}
	}
	return w.text("]]")
}
func (w *nativeValueWriter) stringValue(v string) error {
	if utf8.ValidString(v) {
		if err := w.text(`["utf8",`); err != nil {
			return err
		}
		if err := w.quoted(v); err != nil {
			return err
		}
		return w.text("]")
	}
	if err := w.text(`["bytes_base64","`); err != nil {
		return err
	}
	scratch, err := w.state.ReserveProgramState(w.ctx, 4096)
	if err != nil {
		return err
	}
	if scratch == nil {
		return fmt.Errorf("native encoding requires retained admission")
	}
	defer scratch()
	encoder := base64.NewEncoder(base64.StdEncoding, w)
	var buffer [768]byte
	for len(v) > 0 {
		n := copy(buffer[:], v)
		if _, err := encoder.Write(buffer[:n]); err != nil {
			return err
		}
		v = v[n:]
	}
	if err := encoder.Close(); err != nil {
		return err
	}
	return w.text(`"]`)
}

// quoted preserves the durable JSON escaping without a second scalar buffer.
func (w *nativeValueWriter) quoted(v string) error {
	if err := w.text(`"`); err != nil {
		return err
	}
	start := 0
	const digits = "0123456789abcdef"
	for i := 0; i < len(v); i++ {
		if i&4095 == 0 {
			if err := w.ctx.Err(); err != nil {
				return err
			}
		}
		b := v[i]
		escape := ""
		switch b {
		case '"', '\\':
			escape = "\\" + string(b)
		case '\b':
			escape = `\b`
		case '\f':
			escape = `\f`
		case '\n':
			escape = `\n`
		case '\r':
			escape = `\r`
		case '\t':
			escape = `\t`
		default:
			if b < 0x20 || b == '<' || b == '>' || b == '&' {
				escape = "\\u00" + string([]byte{digits[b>>4], digits[b&15]})
			}
			if b == 0xe2 && i+2 < len(v) && v[i+1] == 0x80 && (v[i+2] == 0xa8 || v[i+2] == 0xa9) {
				escape = "\\u202" + string(digits[v[i+2]&15])
			}
		}
		if escape == "" {
			continue
		}
		if err := w.text(v[start:i]); err != nil {
			return err
		}
		if err := w.text(escape); err != nil {
			return err
		}
		if b == 0xe2 {
			i += 2
		}
		start = i + 1
	}
	if err := w.text(v[start:]); err != nil {
		return err
	}
	return w.text(`"`)
}
