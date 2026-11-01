package toolcontract

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

type nativeValueReader struct {
	ctx    context.Context
	state  ReviewStateAdmission
	source string
	at     int
	leases *reviewLeases
}

func (r *nativeValueReader) take(want string) error {
	if err := r.ctx.Err(); err != nil {
		return err
	}
	if !strings.HasPrefix(r.source[r.at:], want) {
		return fmt.Errorf("invalid native value at byte %d", r.at)
	}
	r.at += len(want)
	return nil
}
func (r *nativeValueReader) next(b byte) bool { return r.at < len(r.source) && r.source[r.at] == b }
func (r *nativeValueReader) quoted() (string, error) {
	if !r.next('"') {
		return "", fmt.Errorf("expected native string at byte %d", r.at)
	}
	start := r.at
	r.at++
	for r.at < len(r.source) {
		if r.at&4095 == 0 {
			if err := r.ctx.Err(); err != nil {
				return "", err
			}
		}
		b := r.source[r.at]
		r.at++
		if b == '\\' {
			if r.at < len(r.source) {
				r.at++
			}
			continue
		}
		if b != '"' {
			continue
		}
		raw := r.source[start:r.at]
		if err := r.leases.reserve(r.ctx, r.state, 2*int64(len(raw))); err != nil {
			return "", err
		}
		var result string
		if err := json.Unmarshal([]byte(raw), &result); err != nil {
			return "", err
		}
		return result, nil
	}
	return "", fmt.Errorf("unterminated native string")
}
func (r *nativeValueReader) value(depth int) (any, error) {
	if depth > nativeValueMaxDepth {
		return nil, fmt.Errorf("native value nesting exceeds %d", nativeValueMaxDepth)
	}
	frame, err := r.state.ReserveProgramState(r.ctx, 512)
	if err != nil {
		return nil, err
	}
	if frame == nil {
		return nil, fmt.Errorf("native decoding requires retained admission")
	}
	defer frame()
	if err := r.leases.reserve(r.ctx, r.state, 32); err != nil {
		return nil, err
	}
	switch {
	case strings.HasPrefix(r.source[r.at:], `["null"]`):
		r.at += 8
		return nil, nil
	case strings.HasPrefix(r.source[r.at:], `["bool",true]`):
		r.at += 13
		return true, nil
	case strings.HasPrefix(r.source[r.at:], `["bool",false]`):
		r.at += 14
		return false, nil
	case strings.HasPrefix(r.source[r.at:], `["array",[`):
		return r.array(depth)
	case strings.HasPrefix(r.source[r.at:], `["object",[`):
		return r.object(depth)
	}
	tag := ""
	for _, candidate := range []string{"utf8", "bytes_base64", "number"} {
		prefix := `["` + candidate + `",`
		if strings.HasPrefix(r.source[r.at:], prefix) {
			tag = candidate
			r.at += len(prefix)
			break
		}
	}
	if tag == "" {
		return nil, fmt.Errorf("invalid native value tag at byte %d", r.at)
	}
	text, err := r.quoted()
	if err != nil {
		return nil, err
	}
	if err := r.take("]"); err != nil {
		return nil, err
	}
	switch tag {
	case "utf8":
		if utf8.ValidString(text) {
			return text, nil
		}
	case "number":
		number := json.Number(text)
		if err := r.leases.reserve(r.ctx, r.state, int64(len(text))); err != nil {
			return nil, err
		}
		raw, err := json.Marshal(number)
		if err == nil && string(raw) == text {
			return number, nil
		}
	case "bytes_base64":
		if err := r.leases.reserve(r.ctx, r.state, 2*int64(len(text))); err != nil {
			return nil, err
		}
		raw, err := base64.StdEncoding.Strict().DecodeString(text)
		if err == nil && !utf8.Valid(raw) && base64.StdEncoding.EncodeToString(raw) == text {
			return string(raw), nil
		}
	}
	return nil, fmt.Errorf("invalid native %s value", tag)
}
func (r *nativeValueReader) array(depth int) (any, error) {
	if err := r.take(`["array",[`); err != nil {
		return nil, err
	}
	var result []any
	for !r.next(']') {
		if len(result) > 0 {
			if err := r.take(","); err != nil {
				return nil, err
			}
		}
		if len(result) == cap(result) {
			capacity := max(4, 2*cap(result))
			if err := r.leases.reserve(r.ctx, r.state, int64(capacity)*16); err != nil {
				return nil, err
			}
			next := make([]any, len(result), capacity)
			copy(next, result)
			result = next
		}
		item, err := r.value(depth + 1)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	if err := r.take("]]"); err != nil {
		return nil, err
	}
	if result == nil {
		result = []any{}
	}
	return result, nil
}
func (r *nativeValueReader) object(depth int) (any, error) {
	if err := r.take(`["object",[`); err != nil {
		return nil, err
	}
	if err := r.leases.reserve(r.ctx, r.state, 256); err != nil {
		return nil, err
	}
	result := map[string]any{}
	previous := ""
	for !r.next(']') {
		if len(result) > 0 {
			if err := r.take(","); err != nil {
				return nil, err
			}
		}
		if err := r.leases.reserve(r.ctx, r.state, 256); err != nil {
			return nil, err
		}
		if err := r.take("["); err != nil {
			return nil, err
		}
		key, err := r.value(depth + 1)
		if err != nil {
			return nil, err
		}
		name, ok := key.(string)
		if !ok {
			return nil, fmt.Errorf("invalid native object key")
		}
		if len(result) > 0 && name <= previous {
			return nil, fmt.Errorf("native object keys are not distinct raw-byte sorted keys")
		}
		previous = name
		if err := r.take(","); err != nil {
			return nil, err
		}
		item, err := r.value(depth + 1)
		if err != nil {
			return nil, err
		}
		if err := r.take("]"); err != nil {
			return nil, err
		}
		result[name] = item
	}
	if err := r.take("]]"); err != nil {
		return nil, err
	}
	return result, nil
}
