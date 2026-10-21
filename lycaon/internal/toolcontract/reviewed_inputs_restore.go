package toolcontract

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"unicode/utf8"
)

// Arguments restores exact original scalar bytes for inspection/screening.
// The caller holds the detached map's lease; live owners retain frozen inputs.
func (r ReviewedOperationInputs) Arguments(ctx context.Context, admission ReviewStateAdmission) (map[string]any, func(), error) {
	if !r.Valid() {
		return nil, nil, fmt.Errorf("admitted operation inputs required")
	}
	value, release, err := r.value.Value(ctx, admission)
	if err != nil {
		return nil, nil, err
	}
	object, ok := value.(map[string]any)
	if !ok {
		release()
		return nil, nil, fmt.Errorf("operation argument root must be an object")
	}
	return object, release, nil
}

// RestoreReviewedOperationInputs accepts only the executable-input domain and
// object shape. Other native value domains cannot acquire operation meaning.
func RestoreReviewedOperationInputs(ctx context.Context, admission ReviewStateAdmission, representation, digest, canonical string) (ReviewedOperationInputs, func(), error) {
	value, release, err := RestoreNativeValue(ctx, admission, OperationArgumentsDomain, representation, digest, canonical)
	if err != nil {
		return ReviewedOperationInputs{}, nil, err
	}
	object, releaseObject, err := value.Value(ctx, admission)
	if err != nil {
		release()
		return ReviewedOperationInputs{}, nil, err
	}
	if _, ok := object.(map[string]any); !ok {
		releaseObject()
		release()
		return ReviewedOperationInputs{}, nil, fmt.Errorf("operation argument root must be an object")
	}
	return ReviewedOperationInputs{value: value}, func() { releaseObject(); release() }, nil
}

func decodeReviewString(value any) (string, error) {
	tagged, ok := value.([]any)
	if !ok || len(tagged) != 2 {
		return "", fmt.Errorf("invalid review string tag")
	}
	text, ok := tagged[1].(string)
	if !ok {
		return "", fmt.Errorf("invalid review string payload")
	}
	switch tagged[0] {
	case "utf8":
		if utf8.ValidString(text) {
			return text, nil
		}
	case "bytes_base64":
		decoded, err := base64.StdEncoding.Strict().DecodeString(text)
		if err == nil && !utf8.Valid(decoded) && base64.StdEncoding.EncodeToString(decoded) == text {
			return string(decoded), nil
		}
	}
	return "", fmt.Errorf("invalid or noncanonical review string")
}

func decodeReviewValue(ctx context.Context, admission ReviewStateAdmission, leases *reviewLeases, value any) (any, error) {
	node, ok := value.([]any)
	if !ok || len(node) < 1 {
		return nil, fmt.Errorf("invalid review value tag")
	}
	if err := leases.reserve(ctx, admission, 0); err != nil {
		return nil, err
	}
	tag, ok := node[0].(string)
	if !ok {
		return nil, fmt.Errorf("invalid review tag")
	}
	if tag == "null" && len(node) == 1 {
		return nil, nil
	}
	if len(node) != 2 {
		return nil, fmt.Errorf("invalid review value arity")
	}
	switch tag {
	case "utf8", "bytes_base64":
		return decodeReviewString(node)
	case "bool":
		v, ok := node[1].(bool)
		if ok {
			return v, nil
		}
	case "number":
		text, ok := node[1].(string)
		if !ok {
			break
		}
		number := json.Number(text)
		raw, err := json.Marshal(number)
		if err == nil && string(raw) == text {
			return number, nil
		}
	case "array":
		items, ok := node[1].([]any)
		if !ok {
			break
		}
		if err := leases.reserve(ctx, admission, int64(len(items))*32); err != nil {
			return nil, err
		}
		result := make([]any, 0, len(items))
		for _, item := range items {
			v, err := decodeReviewValue(ctx, admission, leases, item)
			if err != nil {
				return nil, err
			}
			result = append(result, v)
		}
		return result, nil
	case "object":
		entries, ok := node[1].([]any)
		if !ok {
			break
		}
		if err := leases.reserve(ctx, admission, int64(len(entries))*128); err != nil {
			return nil, err
		}
		result := make(map[string]any, len(entries))
		previous := ""
		for i, entry := range entries {
			pair, ok := entry.([]any)
			if !ok || len(pair) != 2 {
				return nil, fmt.Errorf("invalid review object entry")
			}
			key, err := decodeReviewString(pair[0])
			if err != nil {
				return nil, err
			}
			if i > 0 && key <= previous {
				return nil, fmt.Errorf("review keys are not distinct raw-byte sorted keys")
			}
			previous = key
			item, err := decodeReviewValue(ctx, admission, leases, pair[1])
			if err != nil {
				return nil, err
			}
			result[key] = item
		}
		return result, nil
	}
	return nil, fmt.Errorf("invalid review value payload")
}
