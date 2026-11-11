package toolcontract

import (
	"context"
	"errors"
	"testing"
)

type codecFaultMemory struct {
	held          int64
	calls, failAt int
}

func (m *codecFaultMemory) ReserveProgramState(ctx context.Context, n int64) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.calls++
	if m.calls == m.failAt {
		return nil, errors.New("fixture admission refused")
	}
	m.held += n
	return func() { m.held -= n }, nil
}
func TestNativeCodecReleasesEveryRefusedAdmission(t *testing.T) {
	value := map[string]any{"empty": []any{}, "array": []any{nil, true, false, "escaped\n\b\f<", "raw\xff", []string{"one", "two"}, 12, uint64(99), float32(0.1)}, "object": map[string]any{"x": "value"}}
	reference := &codecFaultMemory{}
	captured, release, err := NewNativeValue(t.Context(), reference, OperationArgumentsDomain, value)
	if err != nil {
		t.Fatalf("reference encode: %v", err)
	}
	encodeCalls := reference.calls
	decodeReference := &codecFaultMemory{}
	_, closeDecoded, err := captured.Value(t.Context(), decodeReference)
	if err != nil {
		t.Fatalf("reference decode: %v", err)
	}
	decodeCalls := decodeReference.calls
	closeDecoded()
	release()
	for at := 1; at <= encodeCalls; at++ {
		memory := &codecFaultMemory{failAt: at}
		_, close, err := NewNativeValue(t.Context(), memory, OperationArgumentsDomain, value)
		if err == nil || close != nil || memory.held != 0 {
			t.Fatalf("encode admission %d: %v held %d", at, err, memory.held)
		}
	}
	for at := 1; at <= decodeCalls; at++ {
		memory := &codecFaultMemory{failAt: at}
		_, close, err := captured.Value(t.Context(), memory)
		if err == nil || close != nil || memory.held != 0 {
			t.Fatalf("decode admission %d: %v held %d", at, err, memory.held)
		}
	}
}
func TestNativeCodecRejectsMalformedCanonicalValues(t *testing.T) {
	for _, raw := range []string{
		`["operation_arguments",1,["unknown"]]`,
		`["operation_arguments",1,["array",[["null"],]]]`,
		`["operation_arguments",1,["object",[[["utf8","b"],["null"]],[["utf8","a"],["null"]]]]]`,
		`["operation_arguments",1,["number","invalid"]]`,
		`["operation_arguments",1,["bytes_base64","YQ=="]]`,
		`["operation_arguments",1,["utf8","unterminated]]`,
	} {
		memory := &codecFaultMemory{}
		candidate := NativeValue{domain: OperationArgumentsDomain, canonical: raw, valid: true}
		_, close, err := candidate.Value(t.Context(), memory)
		if err == nil || close != nil || memory.held != 0 {
			t.Fatalf("malformed value: %s error %v held %d", raw, err, memory.held)
		}
	}
}
