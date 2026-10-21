package toolcontract

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"unicode/utf8"
)

func encodeReviewString(value string) []any {
	if utf8.ValidString(value) {
		return []any{"utf8", value}
	}
	return []any{"bytes_base64", base64.StdEncoding.EncodeToString([]byte(value))}
}

func encodeReviewValue(ctx context.Context, admission ReviewStateAdmission, leases *reviewLeases, value any) (any, error) {
	payload := int64(0)
	switch v := value.(type) {
	case string:
		payload = int64(len(v))
	case map[string]any:
		payload = int64(len(v)) * 128
	case []any:
		payload = int64(len(v)) * 32
	case []string:
		payload = int64(len(v)) * 32
	case json.Number:
		payload = int64(len(v))
	}
	if err := leases.reserve(ctx, admission, payload); err != nil {
		return nil, err
	}
	switch v := value.(type) {
	case nil:
		return []any{"null"}, nil
	case bool:
		return []any{"bool", v}, nil
	case string:
		return encodeReviewString(v), nil
	case map[string]any:
		keys := make([]string, 0, len(v))
		for key := range v {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		entries := make([]any, 0, len(keys))
		for _, key := range keys {
			if err := leases.reserve(ctx, admission, int64(len(key))); err != nil {
				return nil, err
			}
			item, err := encodeReviewValue(ctx, admission, leases, v[key])
			if err != nil {
				return nil, err
			}
			entries = append(entries, []any{encodeReviewString(key), item})
		}
		return []any{"object", entries}, nil
	case []any:
		items := make([]any, 0, len(v))
		for _, item := range v {
			encoded, err := encodeReviewValue(ctx, admission, leases, item)
			if err != nil {
				return nil, err
			}
			items = append(items, encoded)
		}
		return []any{"array", items}, nil
	case []string:
		items := make([]any, 0, len(v))
		for _, item := range v {
			encoded, err := encodeReviewValue(ctx, admission, leases, item)
			if err != nil {
				return nil, err
			}
			items = append(items, encoded)
		}
		return []any{"array", items}, nil
	case json.Number, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64:
		// encoding/json supplies the existing scalar number grammar and width,
		// including json.Number precision and float32's own shortest decimal form.
		number, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		return []any{"number", string(number)}, nil
	default:
		return nil, fmt.Errorf("review arguments require JSON scalar/container values")
	}
}
