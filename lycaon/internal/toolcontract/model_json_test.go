package toolcontract

import (
	"encoding/json"
	"testing"
)

func TestModelJSONRetainsExactCanonicalBytes(t *testing.T) {
	memory := &reviewMemory{maximum: 8 << 20}
	args := map[string]any{"z": []any{"\b\f\u2028<", json.Number("9007199254740993")}, "a": true, "b": []string{"two"}, "nil_array": []any(nil), "nil_strings": []string(nil), "nil_object": map[string]any(nil)}
	got, release, err := CaptureModelJSON(t.Context(), memory, args)
	if err != nil {
		t.Fatalf("capture model: %v", err)
	}
	want, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("reference model: %v", err)
	}
	if got != string(want) || memory.held == 0 {
		t.Fatalf("captured model %q want %q", got, want)
	}
	release()
	if memory.held != 0 {
		t.Fatalf("retained model bytes: %d", memory.held)
	}
}
func TestModelJSONRefusesInvalidValuesWithoutRetention(t *testing.T) {
	for _, value := range []any{"bad\xff", map[string]any{"bad\xff": "key"}, func() {}, json.Number("invalid")} {
		memory := &reviewMemory{maximum: 8 << 20}
		_, release, err := CaptureModelJSON(t.Context(), memory, map[string]any{"value": value})
		if err == nil || release != nil || memory.held != 0 {
			t.Fatalf("invalid model: %v retained %d", err, memory.held)
		}
	}
}

func TestModelJSONReleasesEveryRefusedAdmission(t *testing.T) {
	value := map[string]any{"array": []any{nil, true, false, 12.5, "escaped\n"}, "strings": []string{"one", "two"}, "object": map[string]any{"x": "y"}}
	reference := &codecFaultMemory{}
	_, release, err := CaptureModelJSON(t.Context(), reference, value)
	if err != nil {
		t.Fatalf("reference model: %v", err)
	}
	release()
	for at := 1; at <= reference.calls; at++ {
		memory := &codecFaultMemory{failAt: at}
		_, close, err := CaptureModelJSON(t.Context(), memory, value)
		if err == nil || close != nil || memory.held != 0 {
			t.Fatalf("model admission %d: %v held %d", at, err, memory.held)
		}
	}
}
