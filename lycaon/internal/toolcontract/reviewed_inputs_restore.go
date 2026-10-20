package toolcontract

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

// Arguments restores exact original scalar bytes for inspection/screening.
// The caller owns this detached map and its separate retained-state lease.
// This is never an owner-dispatch path: live owners retain their frozen inputs.
func (r ReviewedOperationInputs) Arguments(ctx context.Context, admission ReviewStateAdmission) (map[string]any, func(), error) {
	if ctx == nil || admission == nil || !r.Valid() {
		return nil, nil, fmt.Errorf("admitted review representation required")
	}
	leases := &reviewLeases{}
	// Account the JSON decoder's tree/scratch before it allocates. Four payload
	// shares reserve 64 bytes per encoded byte; recursive restored frames below
	// are separately admitted before allocating the raw inspection containers.
	if int64(len(r.canonical)) > (int64(^uint64(0)>>1)-2048)/64 {
		return nil, nil, fmt.Errorf("review restore bound overflow")
	}
	if err := leases.reserve(ctx, admission, int64(len(r.canonical))*4); err != nil {
		return nil, nil, err
	}
	decoder := json.NewDecoder(strings.NewReader(r.canonical))
	decoder.UseNumber()
	var envelope []any
	if err := decoder.Decode(&envelope); err != nil {
		leases.close()
		return nil, nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		leases.close()
		return nil, nil, fmt.Errorf("review representation has trailing data")
	}
	if len(envelope) != 3 || envelope[0] != "operation_arguments" || envelope[1] != json.Number("1") {
		leases.close()
		return nil, nil, fmt.Errorf("unknown review argument envelope")
	}
	restored, err := decodeReviewValue(ctx, admission, leases, envelope[2])
	if err != nil {
		leases.close()
		return nil, nil, err
	}
	object, ok := restored.(map[string]any)
	if !ok {
		leases.close()
		return nil, nil, fmt.Errorf("review argument root must be an object")
	}
	return object, leases.release(), nil
}

// RestoreReviewedOperationInputs admits a persisted known representation,
// validates its exact digest and canonical grammar, and refuses unknown shapes.
func RestoreReviewedOperationInputs(ctx context.Context, admission ReviewStateAdmission, representation, digest, canonical string) (ReviewedOperationInputs, func(), error) {
	if representation != LosslessOperationArgumentsV1 {
		return ReviewedOperationInputs{}, nil, fmt.Errorf("unknown review argument representation")
	}
	candidate := ReviewedOperationInputs{representation: representation, digest: digest, canonical: canonical, valid: true}
	arguments, releaseDecoded, err := candidate.Arguments(ctx, admission)
	if err != nil {
		return ReviewedOperationInputs{}, nil, err
	}
	// The parser/tree reservation is live before hashing or copying persisted
	// bytes. This is validation of admitted data, never an owner invocation.
	sum := sha256.Sum256([]byte(canonical))
	if digest != hex.EncodeToString(sum[:]) {
		releaseDecoded()
		return ReviewedOperationInputs{}, nil, fmt.Errorf("review argument digest mismatch")
	}
	normalized, releaseEncoded, err := NewReviewedOperationInputs(ctx, admission, arguments)
	if err != nil {
		releaseDecoded()
		return ReviewedOperationInputs{}, nil, err
	}
	if normalized.canonical != canonical {
		releaseEncoded()
		releaseDecoded()
		return ReviewedOperationInputs{}, nil, fmt.Errorf("noncanonical review argument representation")
	}
	return normalized, func() { releaseEncoded(); releaseDecoded() }, nil
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
